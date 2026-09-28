package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// The API's read path. Nothing here takes a lock or writes: statements and
// reports must never contend with the posting engine.

const accountViewSelect = `
	SELECT a.id, a.public_id, a.code, a.product_id, a.type, a.currency,
	       a.owner_kind, a.owner_id, a.allow_negative, a.status, a.created_at, a.updated_at,
	       p.public_id, b.account_id, b.raw_balance, b.held, b.version, b.updated_at
	FROM ledger_accounts a
	JOIN ledger_balances b ON b.account_id = a.id
	LEFT JOIN products p ON p.id = a.product_id`

// isCode reports whether ref is an account code or journal external id rather
// than a public id. Codes always contain ':' (income:zshala:product_sales);
// public ids never do (acc_0199…).
func isCode(ref string) bool { return strings.Contains(ref, ":") }

// GetAccountView loads an account by public id or code, with its balance.
func (r *Repository) GetAccountView(ctx context.Context, ref string) (*dao.AccountView, error) {
	column := "a.public_id"
	if isCode(ref) {
		column = "a.code"
	}
	view, err := scanAccountView(r.executor(ctx).QueryRowContext(ctx, accountViewSelect+` WHERE `+column+` = $1`, ref))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "ledger account %q not found", ref)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load ledger account")
	}
	return view, nil
}

// ListAccountViews lists accounts with their balances, ordered by code so
// related accounts (psp:razorpay:*, income:zshala:*) sit together.
func (r *Repository) ListAccountViews(ctx context.Context, f *filter.LedgerAccount) ([]*dao.AccountView, int64, error) {
	f.WithDefaults()

	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	switch {
	case f.ProductID != nil:
		where = append(where, "a.product_id = "+arg(*f.ProductID))
	case f.PlatformOnly:
		where = append(where, "a.product_id IS NULL")
	}
	if f.Type != "" {
		where = append(where, "a.type = "+arg(f.Type))
	}
	if f.CodePrefix != "" {
		// Escape LIKE's wildcards: "_" is common in codes (product_sales) and
		// would otherwise match any character.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.CodePrefix)
		where = append(where, "a.code LIKE "+arg(escaped+"%"))
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ledger_accounts a`+clause, args...).Scan(&total); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to count ledger accounts")
	}

	query := accountViewSelect + clause + ` ORDER BY a.code LIMIT ` + arg(f.Limit) + ` OFFSET ` + arg(f.Offset)
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to list ledger accounts")
	}
	defer rows.Close()

	var views []*dao.AccountView
	for rows.Next() {
		view, err := scanAccountView(rows)
		if err != nil {
			return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to scan ledger account")
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to iterate ledger accounts")
	}
	return views, total, nil
}

// ListStatement returns an account's postings newest first, starting below
// beforePostingID (0 for the newest).
//
// Keyset rather than offset pagination: postings arrive continuously, and an
// offset would shift under a reader paging back through history, showing one
// entry twice or skipping one — on a financial statement, both are bugs.
func (r *Repository) ListStatement(ctx context.Context, accountID int64, limit int, beforePostingID int64) ([]*dao.StatementEntry, error) {
	const query = `
		SELECT p.id, p.journal_id, p.account_id, p.direction, p.amount, p.currency,
		       p.seq, p.balance_after, p.created_at,
		       j.public_id, j.external_id, j.kind, COALESCE(j.memo, ''), j.posted_at
		FROM ledger_postings p
		JOIN ledger_journals j ON j.id = p.journal_id
		WHERE p.account_id = $1 AND ($2 = 0 OR p.id < $2)
		ORDER BY p.id DESC
		LIMIT $3`

	rows, err := r.executor(ctx).QueryContext(ctx, query, accountID, beforePostingID, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load statement")
	}
	defer rows.Close()

	var entries []*dao.StatementEntry
	for rows.Next() {
		var (
			e dao.StatementEntry
			p dao.Posting
		)
		if err := rows.Scan(&p.ID, &p.JournalID, &p.AccountID, &p.Direction, &p.Amount, &p.Currency,
			&p.Seq, &p.BalanceAfter, &p.CreatedAt,
			&e.JournalPublicID, &e.JournalExternalID, &e.JournalKind, &e.JournalMemo, &e.JournalPostedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan statement entry")
		}
		e.Posting = &p
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate statement")
	}
	return entries, nil
}

// GetJournalView loads a journal by public id or external id, with its
// postings and the public ids the API shows.
func (r *Repository) GetJournalView(ctx context.Context, ref string) (*dao.Journal, error) {
	column := "j.public_id"
	if isCode(ref) {
		column = "j.external_id"
	}
	query := `
		SELECT j.id, j.public_id, j.external_id, j.kind, j.product_id, j.source_kind, j.source_id,
		       j.reverses_journal_id, COALESCE(j.memo, ''), j.posted_at, j.created_at,
		       p.public_id, rj.public_id
		FROM ledger_journals j
		LEFT JOIN products p ON p.id = j.product_id
		LEFT JOIN ledger_journals rj ON rj.id = j.reverses_journal_id
		WHERE ` + column + ` = $1`

	var j dao.Journal
	err := r.executor(ctx).QueryRowContext(ctx, query, ref).Scan(&j.ID, &j.PublicID, &j.ExternalID, &j.Kind,
		&j.ProductID, &j.SourceKind, &j.SourceID, &j.ReversesJournalID, &j.Memo, &j.PostedAt, &j.CreatedAt,
		&j.ProductPublicID, &j.ReversesJournalPublicID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "journal %q not found", ref)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load journal")
	}

	if j.Postings, err = r.listPostings(ctx, j.ID); err != nil {
		return nil, err
	}
	return &j, nil
}

// TrialBalance sums every account's postings up to asOf.
//
// It reads postings, not ledger_balances, for two reasons: it can answer for a
// past moment, which the materialised balance cannot, and a report computed
// independently of the balances is a second opinion on them rather than a
// restatement.
//
// asOf compares against the posting's timestamp, which PostgreSQL fixes at
// the start of its transaction. A posting whose transaction began just before
// asOf but committed after it is counted — the ledger had not seen it yet at
// asOf, but it happened then.
func (r *Repository) TrialBalance(ctx context.Context, productID *int64, asOf time.Time, includeEmpty bool) ([]*dao.TrialBalanceLine, error) {
	query := `
		SELECT a.public_id, a.code, a.type, pr.public_id, a.currency,
		       COALESCE(SUM(p.amount) FILTER (WHERE p.direction = 1), 0),
		       COALESCE(SUM(p.amount) FILTER (WHERE p.direction = -1), 0)
		FROM ledger_accounts a
		LEFT JOIN products pr ON pr.id = a.product_id
		LEFT JOIN ledger_postings p ON p.account_id = a.id AND p.created_at <= $1
		WHERE ($2::BIGINT IS NULL OR a.product_id = $2)
		GROUP BY a.id, a.public_id, a.code, a.type, pr.public_id, a.currency`
	if !includeEmpty {
		query += ` HAVING COUNT(p.id) > 0`
	}
	query += ` ORDER BY a.code`

	rows, err := r.executor(ctx).QueryContext(ctx, query, asOf, productID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to compute trial balance")
	}
	defer rows.Close()

	var lines []*dao.TrialBalanceLine
	for rows.Next() {
		var l dao.TrialBalanceLine
		if err := rows.Scan(&l.AccountPublicID, &l.Code, &l.Type, &l.ProductPublicID, &l.Currency,
			&l.Debits, &l.Credits); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan trial balance line")
		}
		lines = append(lines, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate trial balance")
	}
	return lines, nil
}

func scanAccountView(row rowScanner) (*dao.AccountView, error) {
	var (
		v dao.AccountView
		a dao.LedgerAccount
	)
	err := row.Scan(&a.ID, &a.PublicID, &a.Code, &a.ProductID, &a.Type, &a.Currency,
		&a.OwnerKind, &a.OwnerID, &a.AllowNegative, &a.Status, &a.CreatedAt, &a.UpdatedAt,
		&v.ProductPublicID, &v.Balance.AccountID, &v.Balance.RawBalance, &v.Balance.Held,
		&v.Balance.Version, &v.Balance.UpdatedAt)
	if err != nil {
		return nil, err
	}
	v.Account = &a
	return &v, nil
}
