package postgresql

import (
	"context"
	"encoding/json"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// ledgerInvariant is one rule the ledger must always satisfy, as a query that
// returns the rows breaking it: (subject, expected, actual, detail).
type ledgerInvariant struct {
	name  string
	query string
}

// ledgerInvariants are checked in this order. Each query lists violations and
// nothing else, so a healthy ledger returns no rows from any of them.
//
// None of them can be enforced by the posting engine alone: they are what
// would catch a bug in it, a manual edit to ledger_balances, or a migration
// that forgot the triggers.
var ledgerInvariants = []ledgerInvariant{
	{
		// D2: every journal sums to zero, separately in each currency.
		name: "journal_unbalanced",
		query: `
			SELECT j.external_id, 0, SUM(p.direction * p.amount), 'currency ' || p.currency
			FROM ledger_postings p
			JOIN ledger_journals j ON j.id = p.journal_id
			GROUP BY j.id, j.external_id, p.currency
			HAVING SUM(p.direction * p.amount) <> 0
			ORDER BY j.id`,
	},
	{
		// A journal is a movement between at least two accounts; one with fewer
		// postings is a half-written transaction.
		name: "journal_too_few_postings",
		query: `
			SELECT j.external_id, 2, COUNT(p.id), ''
			FROM ledger_journals j
			LEFT JOIN ledger_postings p ON p.journal_id = j.id
			GROUP BY j.id, j.external_id
			HAVING COUNT(p.id) < 2
			ORDER BY j.id`,
	},
	{
		// The whole ledger sums to zero per currency. Implied by the first check,
		// and checked anyway: it is the one number an auditor asks for first.
		name: "ledger_unbalanced",
		query: `
			SELECT currency, 0, SUM(direction * amount), ''
			FROM ledger_postings
			GROUP BY currency
			HAVING SUM(direction * amount) <> 0
			ORDER BY currency`,
	},
	{
		// The materialised balance equals the sum of the account's postings.
		name: "balance_drift",
		query: `
			SELECT a.code, COALESCE(SUM(p.direction * p.amount), 0), b.raw_balance, 'raw balance'
			FROM ledger_accounts a
			JOIN ledger_balances b ON b.account_id = a.id
			LEFT JOIN ledger_postings p ON p.account_id = a.id
			GROUP BY a.id, a.code, b.raw_balance
			HAVING b.raw_balance <> COALESCE(SUM(p.direction * p.amount), 0)
			ORDER BY a.id`,
	},
	{
		// Every posting's balance_after equals the running sum to that point, so
		// a statement's running balance can be trusted line by line.
		name: "running_balance_drift",
		query: `
			SELECT code, running, balance_after, 'posting ' || id
			FROM (
				SELECT a.code, p.id, p.balance_after,
				       SUM(p.direction * p.amount) OVER (PARTITION BY p.account_id ORDER BY p.id) AS running
				FROM ledger_postings p
				JOIN ledger_accounts a ON a.id = p.account_id
			) r
			WHERE running <> balance_after
			ORDER BY id`,
	},
	{
		// A posting is in its account's currency; otherwise the account's balance
		// is a sum of two different things.
		name: "posting_currency_mismatch",
		query: `
			SELECT a.code, 0, p.amount, 'posting ' || p.id || ' in ' || p.currency || ', account in ' || a.currency
			FROM ledger_postings p
			JOIN ledger_accounts a ON a.id = p.account_id
			WHERE p.currency <> a.currency
			ORDER BY p.id`,
	},
	{
		// held equals the sum of the account's active holds.
		name: "held_drift",
		query: `
			SELECT a.code, COALESCE(SUM(h.amount), 0), b.held, 'held'
			FROM ledger_accounts a
			JOIN ledger_balances b ON b.account_id = a.id
			LEFT JOIN ledger_holds h ON h.account_id = a.id AND h.status = 'active'
			GROUP BY a.id, a.code, b.held
			HAVING b.held <> COALESCE(SUM(h.amount), 0)
			ORDER BY a.id`,
	},
	{
		// An account that may not go negative has not. Checked on what is
		// available, since a hold is as binding as a posting.
		name: "negative_balance",
		query: `
			SELECT a.code, 0, n.natural - b.held, 'balance ' || n.natural || ', held ' || b.held
			FROM ledger_accounts a
			JOIN ledger_balances b ON b.account_id = a.id
			CROSS JOIN LATERAL (
				SELECT b.raw_balance * CASE WHEN a.type IN ('asset', 'expense') THEN 1 ELSE -1 END AS natural
			) n
			WHERE NOT a.allow_negative AND n.natural - b.held < 0
			ORDER BY a.id`,
	},
	{
		// Every account has a balance row, or the posting engine cannot lock it.
		name: "missing_balance_row",
		query: `
			SELECT a.code, 1, 0, ''
			FROM ledger_accounts a
			LEFT JOIN ledger_balances b ON b.account_id = a.id
			WHERE b.account_id IS NULL
			ORDER BY a.id`,
	},
}

// CheckLedgerInvariants runs every invariant against one snapshot, so a run's
// findings all describe the same moment of the ledger.
//
// Each invariant must compare both of its sides inside one statement. Split
// across two queries, a journal committing between them would read as drift.
//
// At most limit findings are kept per invariant. truncated reports that some
// invariant had more: a ledger that broken needs a person, not a longer list.
func (r *Repository) CheckLedgerInvariants(ctx context.Context, limit int) (findings []dao.LedgerCheckFinding, truncated bool, err error) {
	err = r.withReadSnapshot(ctx, func(ctx context.Context) error {
		for _, invariant := range ledgerInvariants {
			found, more, err := r.checkInvariant(ctx, invariant, limit)
			if err != nil {
				return err
			}
			findings = append(findings, found...)
			truncated = truncated || more
		}
		return nil
	})
	return findings, truncated, err
}

func (r *Repository) checkInvariant(ctx context.Context, invariant ledgerInvariant, limit int) ([]dao.LedgerCheckFinding, bool, error) {
	// One extra row reveals whether there were more than limit.
	rows, err := r.executor(ctx).QueryContext(ctx, invariant.query+` LIMIT $1`, limit+1)
	if err != nil {
		return nil, false, apperrors.Wrap(err, apperrors.Internal, "failed to check %s", invariant.name)
	}
	defer rows.Close()

	var findings []dao.LedgerCheckFinding
	for rows.Next() {
		f := dao.LedgerCheckFinding{Invariant: invariant.name}
		if err := rows.Scan(&f.Subject, &f.Expected, &f.Actual, &f.Detail); err != nil {
			return nil, false, apperrors.Wrap(err, apperrors.Internal, "failed to scan %s finding", invariant.name)
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, false, apperrors.Wrap(err, apperrors.Internal, "failed to iterate %s findings", invariant.name)
	}

	if len(findings) > limit {
		return findings[:limit], true, nil
	}
	return findings, false, nil
}

// RecordLedgerCheckRun stores the outcome of a check.
func (r *Repository) RecordLedgerCheckRun(ctx context.Context, run *dao.LedgerCheckRun) error {
	findings := run.Findings
	if findings == nil {
		findings = []dao.LedgerCheckFinding{}
	}
	encoded, err := json.Marshal(findings)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to encode ledger check findings")
	}

	const insert = `
		INSERT INTO ledger_check_runs (public_id, status, trigger, triggered_by, findings,
		                               truncated, error, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`
	if err := r.executor(ctx).QueryRowContext(ctx, insert,
		run.PublicID, run.Status, run.Trigger, run.TriggeredBy, encoded,
		run.Truncated, run.Error, run.StartedAt, run.FinishedAt,
	).Scan(&run.ID); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to record ledger check run")
	}
	return nil
}

// ListLedgerCheckRuns returns the most recent runs, newest first.
func (r *Repository) ListLedgerCheckRuns(ctx context.Context, limit int) ([]*dao.LedgerCheckRun, error) {
	const query = `
		SELECT id, public_id, status, trigger, triggered_by, findings, truncated, error,
		       started_at, finished_at
		FROM ledger_check_runs
		ORDER BY id DESC
		LIMIT $1`

	rows, err := r.executor(ctx).QueryContext(ctx, query, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list ledger check runs")
	}
	defer rows.Close()

	var runs []*dao.LedgerCheckRun
	for rows.Next() {
		var (
			run      dao.LedgerCheckRun
			findings []byte
		)
		if err := rows.Scan(&run.ID, &run.PublicID, &run.Status, &run.Trigger, &run.TriggeredBy,
			&findings, &run.Truncated, &run.Error, &run.StartedAt, &run.FinishedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan ledger check run")
		}
		if err := json.Unmarshal(findings, &run.Findings); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to decode ledger check findings")
		}
		runs = append(runs, &run)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate ledger check runs")
	}
	return runs, nil
}
