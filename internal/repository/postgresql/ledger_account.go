package postgresql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const ledgerAccountColumns = `id, public_id, code, product_id, type, currency,
	owner_kind, owner_id, allow_negative, status, created_at, updated_at`

// CreateLedgerAccount opens an account and its balance row together.
//
// The balance row is created here rather than lazily, because the posting
// engine locks balances with FOR UPDATE and a row that does not exist yet
// cannot be locked — two concurrent first postings to a new account would then
// both create one.
func (r *Repository) CreateLedgerAccount(ctx context.Context, account *dao.LedgerAccount) error {
	const insertAccount = `
		INSERT INTO ledger_accounts (public_id, code, product_id, type, currency,
		                             owner_kind, owner_id, allow_negative, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`

	const insertBalance = `INSERT INTO ledger_balances (account_id) VALUES ($1)`

	err := r.executor(ctx).QueryRowContext(ctx, insertAccount,
		account.PublicID, account.Code, account.ProductID, account.Type, account.Currency,
		account.OwnerKind, account.OwnerID, account.AllowNegative, account.Status,
	).Scan(&account.ID, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists,
				"ledger account %q already exists", account.Code)
		}
		if isForeignKeyViolation(err) {
			return apperrors.Wrap(err, apperrors.NotFound, "product does not exist")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create ledger account")
	}

	if _, err := r.executor(ctx).ExecContext(ctx, insertBalance, account.ID); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create balance row for %q", account.Code)
	}
	return nil
}

// GetOrCreateLedgerAccount opens an account if it is not already there.
//
// Wallets and provider accounts are created on first use, and two concurrent
// first uses are ordinary. The unique constraint decides the winner; the loser
// reads the winner's row rather than failing.
func (r *Repository) GetOrCreateLedgerAccount(ctx context.Context, account *dao.LedgerAccount) error {
	existing, err := r.GetLedgerAccountByCode(ctx, account.Code)
	if err == nil {
		*account = *existing
		return nil
	}
	if !apperrors.Is(err, apperrors.NotFound) {
		return err
	}

	err = r.CreateLedgerAccount(ctx, account)
	if err == nil {
		return nil
	}
	if !apperrors.Is(err, apperrors.AlreadyExists) {
		return err
	}

	existing, err = r.GetLedgerAccountByCode(ctx, account.Code)
	if err != nil {
		return err
	}
	*account = *existing
	return nil
}

func (r *Repository) GetLedgerAccountByCode(ctx context.Context, code string) (*dao.LedgerAccount, error) {
	const query = `SELECT ` + ledgerAccountColumns + ` FROM ledger_accounts WHERE code = $1`

	account, err := scanLedgerAccount(r.executor(ctx).QueryRowContext(ctx, query, code))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "ledger account %q not found", code)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load ledger account")
	}
	return account, nil
}

// GetBalance returns an account's materialised position.
func (r *Repository) GetBalance(ctx context.Context, accountID int64) (*dao.Balance, error) {
	const query = `
		SELECT account_id, raw_balance, held, version, updated_at
		FROM ledger_balances WHERE account_id = $1`

	var b dao.Balance
	err := r.executor(ctx).QueryRowContext(ctx, query, accountID).
		Scan(&b.AccountID, &b.RawBalance, &b.Held, &b.Version, &b.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "no balance for ledger account %d", accountID)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load balance")
	}
	return &b, nil
}

// ListAccountPostings returns an account's statement, newest first.
func (r *Repository) ListAccountPostings(ctx context.Context, accountID int64, limit, offset int) ([]*dao.Posting, error) {
	const query = `
		SELECT p.id, p.journal_id, p.account_id, a.code, p.direction, p.amount,
		       p.currency, p.seq, p.balance_after, p.created_at
		FROM ledger_postings p
		JOIN ledger_accounts a ON a.id = p.account_id
		WHERE p.account_id = $1
		ORDER BY p.id DESC
		LIMIT $2 OFFSET $3`

	rows, err := r.executor(ctx).QueryContext(ctx, query, accountID, limit, offset)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list account postings")
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

func scanLedgerAccount(row rowScanner) (*dao.LedgerAccount, error) {
	var a dao.LedgerAccount
	err := row.Scan(&a.ID, &a.PublicID, &a.Code, &a.ProductID, &a.Type, &a.Currency,
		&a.OwnerKind, &a.OwnerID, &a.AllowNegative, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}
