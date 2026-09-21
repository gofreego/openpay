package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/lib/pq"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// PostJournal writes one balanced transaction.
//
// This is the only way money moves in OpenPay. It must run inside a
// transaction: the journal, its postings and the balance updates commit
// together or not at all.
//
// It is idempotent on the journal's ExternalID. A second call with the same
// external id returns the journal already posted rather than posting it again,
// which is what makes a retried payment capture safe.
func (r *Repository) PostJournal(ctx context.Context, journal *dao.Journal) error {
	if !InTx(ctx) {
		return apperrors.New(apperrors.Internal,
			"PostJournal must run inside a transaction: a journal and its postings cannot be allowed to commit separately")
	}
	if err := validateJournal(journal); err != nil {
		return err
	}

	// Claim the external id first. If another journal already owns it, this
	// work has been done and must not be repeated.
	posted, existing, err := r.insertJournal(ctx, journal)
	if err != nil {
		return err
	}
	if !posted {
		*journal = *existing
		return nil
	}

	accounts, err := r.lockAccountsForPosting(ctx, journal)
	if err != nil {
		return err
	}

	return r.writePostings(ctx, journal, accounts)
}

// validateJournal checks what must be true of any journal before the database
// is touched at all.
func validateJournal(journal *dao.Journal) error {
	if journal.ExternalID == "" {
		return apperrors.New(apperrors.Internal, "a journal must carry an external id to be idempotent")
	}
	if len(journal.Postings) < 2 {
		return apperrors.New(apperrors.LedgerImbalance,
			"a journal needs at least two postings, got %d", len(journal.Postings))
	}

	// Sum per currency, not overall: a journal that nets to zero only by
	// cancelling rupees against dollars is not balanced, it is two mistakes.
	sums := map[string]int64{}
	for i, posting := range journal.Postings {
		if posting.Amount <= 0 {
			return apperrors.New(apperrors.LedgerImbalance,
				"posting %d has a non-positive amount (%d); direction carries the sign", i, posting.Amount)
		}
		if posting.Direction != dao.Debit && posting.Direction != dao.Credit {
			return apperrors.New(apperrors.LedgerImbalance,
				"posting %d has an invalid direction %d", i, posting.Direction)
		}
		if posting.Currency == "" {
			return apperrors.New(apperrors.LedgerImbalance, "posting %d has no currency", i)
		}
		sums[posting.Currency] += posting.Signed()
	}

	for currency, sum := range sums {
		if sum != 0 {
			return apperrors.New(apperrors.LedgerImbalance,
				"journal %q does not balance in %s: debits minus credits is %d, must be 0",
				journal.ExternalID, currency, sum)
		}
	}
	return nil
}

// insertJournal claims the external id, reporting whether this call won it.
func (r *Repository) insertJournal(ctx context.Context, journal *dao.Journal) (posted bool, existing *dao.Journal, err error) {
	const insert = `
		INSERT INTO ledger_journals (public_id, external_id, kind, product_id,
		                             source_kind, source_id, reverses_journal_id, memo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (external_id) DO NOTHING
		RETURNING id, posted_at, created_at`

	err = r.executor(ctx).QueryRowContext(ctx, insert,
		journal.PublicID, journal.ExternalID, journal.Kind, journal.ProductID,
		journal.SourceKind, journal.SourceID, journal.ReversesJournalID, journal.Memo,
	).Scan(&journal.ID, &journal.PostedAt, &journal.CreatedAt)
	if err == nil {
		return true, nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, nil, apperrors.Wrap(err, apperrors.Internal, "failed to create journal")
	}

	existing, err = r.GetJournalByExternalID(ctx, journal.ExternalID)
	if err != nil {
		return false, nil, err
	}
	return false, existing, nil
}

// lockAccountsForPosting loads and locks every account the journal touches.
//
// The lock order is what prevents deadlock. Two journals touching the same pair
// of accounts in opposite orders would each hold what the other needs, so rows
// are always locked by ascending account id — an order both transactions agree
// on without knowing about each other.
func (r *Repository) lockAccountsForPosting(ctx context.Context, journal *dao.Journal) (map[int64]*lockedAccount, error) {
	ids := make([]int64, 0, len(journal.Postings))
	for _, posting := range journal.Postings {
		if !slices.Contains(ids, posting.AccountID) {
			ids = append(ids, posting.AccountID)
		}
	}
	slices.Sort(ids)

	const query = `
		SELECT a.id, a.code, a.type, a.currency, a.allow_negative, a.status,
		       b.raw_balance, b.held
		FROM ledger_accounts a
		JOIN ledger_balances b ON b.account_id = a.id
		WHERE a.id = ANY($1)
		ORDER BY a.id
		FOR UPDATE OF b`

	rows, err := r.executor(ctx).QueryContext(ctx, query, pq.Array(ids))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to lock accounts for posting")
	}
	defer rows.Close()

	accounts := make(map[int64]*lockedAccount, len(ids))
	for rows.Next() {
		var a lockedAccount
		if err := rows.Scan(&a.id, &a.code, &a.accountType, &a.currency,
			&a.allowNegative, &a.status, &a.rawBalance, &a.held); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan locked account")
		}
		accounts[a.id] = &a
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate locked accounts")
	}

	for _, id := range ids {
		account, ok := accounts[id]
		if !ok {
			return nil, apperrors.New(apperrors.NotFound, "ledger account %d does not exist", id)
		}
		if account.status != dao.AccountActive {
			return nil, apperrors.New(apperrors.FailedPrecondition,
				"ledger account %q is closed and cannot be posted to", account.code)
		}
	}
	return accounts, nil
}

type lockedAccount struct {
	id            int64
	code          string
	accountType   dao.AccountType
	currency      string
	allowNegative bool
	status        dao.AccountStatus
	rawBalance    int64
	held          int64
}

// writePostings applies the legs and the resulting balances.
func (r *Repository) writePostings(ctx context.Context, journal *dao.Journal, accounts map[int64]*lockedAccount) error {
	const insertPosting = `
		INSERT INTO ledger_postings (journal_id, account_id, direction, amount, currency, seq, balance_after)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`

	const updateBalance = `
		UPDATE ledger_balances
		SET raw_balance = $2, version = version + 1, updated_at = NOW()
		WHERE account_id = $1`

	for seq, posting := range journal.Postings {
		account := accounts[posting.AccountID]

		// A posting in a currency its account does not hold would corrupt the
		// balance silently: the number would still add up, but it would be the
		// sum of two different things.
		if posting.Currency != account.currency {
			return apperrors.New(apperrors.CurrencyMismatch,
				"posting in %s cannot go to account %q, which is denominated in %s",
				posting.Currency, account.code, account.currency)
		}

		account.rawBalance += posting.Signed()

		// Overdraft is checked against the natural balance, because "negative"
		// means owing value for an asset and holding it for a liability.
		if !account.allowNegative {
			natural := account.rawBalance * account.accountType.NormalSign()
			if natural < 0 {
				return apperrors.New(apperrors.InsufficientBalance,
					"account %q would go to %d, and it does not permit a negative balance",
					account.code, natural)
			}
		}

		posting.JournalID = journal.ID
		posting.Seq = seq
		posting.BalanceAfter = account.rawBalance

		if err := r.executor(ctx).QueryRowContext(ctx, insertPosting,
			posting.JournalID, posting.AccountID, posting.Direction, posting.Amount,
			posting.Currency, posting.Seq, posting.BalanceAfter,
		).Scan(&posting.ID, &posting.CreatedAt); err != nil {
			return apperrors.Wrap(err, apperrors.Internal, "failed to write posting %d", seq)
		}
	}

	// Balances are written once per account after all its postings, so an
	// account appearing twice in one journal ends at the right figure.
	for _, account := range accounts {
		if _, err := r.executor(ctx).ExecContext(ctx, updateBalance, account.id, account.rawBalance); err != nil {
			return apperrors.Wrap(err, apperrors.Internal, "failed to update balance for %q", account.code)
		}
	}
	return nil
}

// GetJournalByExternalID loads a journal and its postings.
func (r *Repository) GetJournalByExternalID(ctx context.Context, externalID string) (*dao.Journal, error) {
	const query = `
		SELECT id, public_id, external_id, kind, product_id, source_kind, source_id,
		       reverses_journal_id, memo, posted_at, created_at
		FROM ledger_journals WHERE external_id = $1`

	journal, err := scanJournal(r.executor(ctx).QueryRowContext(ctx, query, externalID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "journal %q not found", externalID)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load journal")
	}

	if journal.Postings, err = r.listPostings(ctx, journal.ID); err != nil {
		return nil, err
	}
	return journal, nil
}

func (r *Repository) listPostings(ctx context.Context, journalID int64) ([]*dao.Posting, error) {
	const query = `
		SELECT p.id, p.journal_id, p.account_id, a.code, p.direction, p.amount,
		       p.currency, p.seq, p.balance_after, p.created_at
		FROM ledger_postings p
		JOIN ledger_accounts a ON a.id = p.account_id
		WHERE p.journal_id = $1
		ORDER BY p.seq`

	rows, err := r.executor(ctx).QueryContext(ctx, query, journalID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list postings")
	}
	defer rows.Close()

	var postings []*dao.Posting
	for rows.Next() {
		var p dao.Posting
		if err := rows.Scan(&p.ID, &p.JournalID, &p.AccountID, &p.AccountCode, &p.Direction,
			&p.Amount, &p.Currency, &p.Seq, &p.BalanceAfter, &p.CreatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan posting")
		}
		postings = append(postings, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate postings")
	}
	return postings, nil
}

func scanJournal(row rowScanner) (*dao.Journal, error) {
	var j dao.Journal
	err := row.Scan(&j.ID, &j.PublicID, &j.ExternalID, &j.Kind, &j.ProductID,
		&j.SourceKind, &j.SourceID, &j.ReversesJournalID, &j.Memo, &j.PostedAt, &j.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}
