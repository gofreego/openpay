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

const walletColumns = `w.id, w.public_id, w.customer_id, c.public_id, w.wallet_type_id, w.product_id,
	w.ledger_account_id, w.status, w.created_at, w.updated_at`

const walletFrom = ` FROM wallets w JOIN customers c ON c.id = w.customer_id`

// GetOrCreateWallet opens a wallet unless the customer already has one of
// that type, in which case it loads that one into wallet.
//
// Wallets are created on first reference, and two first references at once
// are ordinary. The unique (customer, type) constraint decides the winner
// without raising an error, which inside a transaction would abort it.
func (r *Repository) GetOrCreateWallet(ctx context.Context, wallet *dao.Wallet) (created bool, err error) {
	const insert = `
		INSERT INTO wallets (public_id, customer_id, wallet_type_id, product_id, ledger_account_id, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (customer_id, wallet_type_id) DO NOTHING
		RETURNING id, created_at, updated_at`

	err = r.executor(ctx).QueryRowContext(ctx, insert,
		wallet.PublicID, wallet.CustomerID, wallet.WalletTypeID, wallet.ProductID,
		wallet.LedgerAccountID, wallet.Status,
	).Scan(&wallet.ID, &wallet.CreatedAt, &wallet.UpdatedAt)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		if isForeignKeyViolation(err) {
			return false, apperrors.Wrap(err, apperrors.NotFound, "customer, wallet type or ledger account does not exist")
		}
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to create wallet")
	}

	existing, err := r.GetWallet(ctx, wallet.CustomerID, wallet.WalletTypeID)
	if err != nil {
		return false, err
	}
	*wallet = *existing
	return false, nil
}

func (r *Repository) GetWallet(ctx context.Context, customerID, walletTypeID int64) (*dao.Wallet, error) {
	const query = `SELECT ` + walletColumns + walletFrom + ` WHERE w.customer_id = $1 AND w.wallet_type_id = $2`
	wallet, err := scanWallet(r.executor(ctx).QueryRowContext(ctx, query, customerID, walletTypeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "customer %d has no wallet of type %d", customerID, walletTypeID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load wallet")
	}
	return wallet, nil
}

func (r *Repository) GetWalletByPublicID(ctx context.Context, publicID string) (*dao.Wallet, error) {
	const query = `SELECT ` + walletColumns + walletFrom + ` WHERE w.public_id = $1`
	wallet, err := scanWallet(r.executor(ctx).QueryRowContext(ctx, query, publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "wallet %q not found", publicID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load wallet")
	}
	return wallet, nil
}

// ListCustomerWallets returns a customer's wallets within scope.
//
// includePlatform adds platform-scoped wallets, which are spendable in every
// product: a product backend asking for its customer's wallets must see them,
// while a product operator must not (they belong to no one product).
//
// This is the one query where a platform-wide customer could leak one
// product's balances into another's app (plan.md D8), so the scope is
// mandatory here as everywhere.
func (r *Repository) ListCustomerWallets(ctx context.Context, customerID int64, scope *filter.ProductScope, includePlatform bool) ([]*dao.Wallet, error) {
	args := []any{customerID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "w.product_id", arg)
	if err != nil {
		return nil, err
	}

	var visible []string
	switch scoped {
	case "":
		visible = append(visible, "TRUE")
	default:
		visible = append(visible, scoped)
	}
	if includePlatform {
		visible = append(visible, "w.product_id IS NULL")
	}

	query := `SELECT ` + walletColumns + walletFrom + `
		WHERE w.customer_id = $1 AND (` + strings.Join(visible, " OR ") + `)
		ORDER BY w.product_id NULLS LAST, w.id`

	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list wallets")
	}
	defer rows.Close()

	var wallets []*dao.Wallet
	for rows.Next() {
		wallet, err := scanWallet(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan wallet")
		}
		wallets = append(wallets, wallet)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate wallets")
	}
	return wallets, nil
}

// SumWalletLoadsSince totals what was loaded into a wallet account since a
// moment: top-ups, grants and incoming transfers. Refunds and reversals are
// excluded — returning the customer's own money is not loading more of it.
//
// Called under the account's lock (PostJournalChecked), so the total cannot
// change between this read and the decision made on it.
func (r *Repository) SumWalletLoadsSince(ctx context.Context, accountID int64, since time.Time) (int64, error) {
	const query = `
		SELECT COALESCE(SUM(p.amount), 0)
		FROM ledger_postings p
		JOIN ledger_journals j ON j.id = p.journal_id
		WHERE p.account_id = $1
		  AND p.direction = -1
		  AND p.created_at >= $2
		  AND j.kind IN ('topup', 'grant', 'transfer')`

	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, accountID, since).Scan(&total); err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to total wallet loads")
	}
	return total, nil
}

func scanWallet(row rowScanner) (*dao.Wallet, error) {
	var w dao.Wallet
	err := row.Scan(&w.ID, &w.PublicID, &w.CustomerID, &w.CustomerPublicID, &w.WalletTypeID, &w.ProductID,
		&w.LedgerAccountID, &w.Status, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ListRollingExpiryCandidates finds wallets of rolling-expiry types whose
// last posting is older than the type's expiry_days and that still hold
// value. Wallets with active holds are skipped: a hold is activity, and
// expiring value an open checkout has reserved would fail that checkout.
func (r *Repository) ListRollingExpiryCandidates(ctx context.Context, now time.Time, limit int) ([]dao.ExpiryCandidate, error) {
	const query = `
		SELECT w.public_id, last.posting_id, -b.raw_balance
		FROM wallets w
		JOIN wallet_types wt ON wt.id = w.wallet_type_id
		JOIN ledger_balances b ON b.account_id = w.ledger_account_id
		CROSS JOIN LATERAL (
			SELECT p.id AS posting_id, p.created_at
			FROM ledger_postings p
			WHERE p.account_id = w.ledger_account_id
			ORDER BY p.id DESC
			LIMIT 1
		) last
		WHERE wt.expiry_policy = 'rolling'
		  AND w.product_id IS NOT NULL
		  AND b.raw_balance < 0
		  AND b.held = 0
		  AND last.created_at < $1::TIMESTAMPTZ - make_interval(days => wt.expiry_days)
		ORDER BY last.created_at
		LIMIT $2`

	rows, err := r.executor(ctx).QueryContext(ctx, query, now, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to find expiring wallets")
	}
	defer rows.Close()

	var candidates []dao.ExpiryCandidate
	for rows.Next() {
		var c dao.ExpiryCandidate
		if err := rows.Scan(&c.WalletPublicID, &c.LastPostingID, &c.Balance); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan expiring wallet")
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate expiring wallets")
	}
	return candidates, nil
}

// LatestPostingID is the newest posting on an account other than those of
// one journal — used to confirm nothing moved between deciding to expire a
// wallet and doing it.
func (r *Repository) LatestPostingID(ctx context.Context, accountID, excludingJournalID int64) (int64, error) {
	const query = `
		SELECT COALESCE(MAX(id), 0) FROM ledger_postings
		WHERE account_id = $1 AND journal_id <> $2`
	var id int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, accountID, excludingJournalID).Scan(&id); err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to load latest posting")
	}
	return id, nil
}

// LockCustomer serialises limit checks that span a customer's wallets.
//
// Always taken after the ledger locks (from a PostJournalChecked hook) and
// never before, so every path agrees on the order and none can deadlock.
func (r *Repository) LockCustomer(ctx context.Context, customerID int64) error {
	var id int64
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT id FROM customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&id); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to lock customer %d", customerID)
	}
	return nil
}

// CustomerFundedBalance totals a customer's balances across every fundable
// wallet, in every product: the real money we hold for one person.
func (r *Repository) CustomerFundedBalance(ctx context.Context, customerID int64) (int64, error) {
	const query = `
		SELECT COALESCE(SUM(-b.raw_balance), 0)
		FROM wallets w
		JOIN wallet_types wt ON wt.id = w.wallet_type_id AND wt.fundable
		JOIN ledger_balances b ON b.account_id = w.ledger_account_id
		WHERE w.customer_id = $1`
	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, customerID).Scan(&total); err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to total customer balance")
	}
	return total, nil
}

// CustomerFundedLoadsSince totals what was loaded into a customer's fundable
// wallets, across every product, since a moment.
func (r *Repository) CustomerFundedLoadsSince(ctx context.Context, customerID int64, since time.Time) (int64, error) {
	const query = `
		SELECT COALESCE(SUM(p.amount), 0)
		FROM wallets w
		JOIN wallet_types wt ON wt.id = w.wallet_type_id AND wt.fundable
		JOIN ledger_postings p ON p.account_id = w.ledger_account_id
		JOIN ledger_journals j ON j.id = p.journal_id
		WHERE w.customer_id = $1
		  AND p.direction = -1
		  AND p.created_at >= $2
		  AND j.kind IN ('topup', 'grant', 'transfer')`
	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, query, customerID, since).Scan(&total); err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to total customer loads")
	}
	return total, nil
}
