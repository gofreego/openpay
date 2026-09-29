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

const beneficiaryColumns = `id, public_id, customer_id, kind, name, account_number, account_last4, account_fingerprint, ifsc, vpa, status,
	name_at_bank, verified_at, failure_reason, created_at, updated_at`

// CreateBeneficiary registers a destination. The same destination for the
// same customer is registered once (AlreadyExists the second time).
func (r *Repository) CreateBeneficiary(ctx context.Context, b *dao.Beneficiary) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO beneficiaries (public_id, customer_id, kind, name, account_number, account_last4, account_fingerprint,
		                           ifsc, vpa, status, name_at_bank, verified_at, failure_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at`,
		b.PublicID, b.CustomerID, b.Kind, b.Name, b.AccountNumber, b.AccountLast4, b.AccountFingerprint, b.IFSC, b.VPA, b.Status,
		b.NameAtBank, b.VerifiedAt, b.FailureReason,
	).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists, "this destination is already registered for the customer")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create beneficiary")
	}
	return nil
}

func (r *Repository) GetBeneficiaryByPublicID(ctx context.Context, publicID string) (*dao.Beneficiary, error) {
	b, err := scanBeneficiary(r.executor(ctx).QueryRowContext(ctx,
		`SELECT `+beneficiaryColumns+` FROM beneficiaries WHERE public_id = $1`, publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "beneficiary %q not found", publicID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load beneficiary")
	}
	return b, nil
}

func (r *Repository) GetBeneficiaryByID(ctx context.Context, id int64) (*dao.Beneficiary, error) {
	b, err := scanBeneficiary(r.executor(ctx).QueryRowContext(ctx,
		`SELECT `+beneficiaryColumns+` FROM beneficiaries WHERE id = $1`, id))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load beneficiary %d", id)
	}
	return b, nil
}

func (r *Repository) ListBeneficiaries(ctx context.Context, customerID int64) ([]*dao.Beneficiary, error) {
	rows, err := r.executor(ctx).QueryContext(ctx,
		`SELECT `+beneficiaryColumns+` FROM beneficiaries WHERE customer_id = $1 ORDER BY id`, customerID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list beneficiaries")
	}
	defer rows.Close()
	var out []*dao.Beneficiary
	for rows.Next() {
		b, err := scanBeneficiary(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan beneficiary")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBeneficiary(row rowScanner) (*dao.Beneficiary, error) {
	var b dao.Beneficiary
	err := row.Scan(&b.ID, &b.PublicID, &b.CustomerID, &b.Kind, &b.Name, &b.AccountNumber, &b.AccountLast4, &b.AccountFingerprint,
		&b.IFSC, &b.VPA, &b.Status,
		&b.NameAtBank, &b.VerifiedAt, &b.FailureReason, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ---- Withdrawals ----

const withdrawalColumns = `x.id, x.public_id, x.wallet_id, x.customer_id, x.product_id, x.beneficiary_id,
	x.amount, x.currency, x.status, x.requires_approval, x.requested_by, x.decided_by, x.decided_at,
	x.decision_note, x.provider, x.provider_payout_id, x.failure_reason, x.paid_at,
	w.public_id, b.public_id, x.created_at, x.updated_at`

const withdrawalFrom = ` FROM withdrawals x JOIN wallets w ON w.id = x.wallet_id JOIN beneficiaries b ON b.id = x.beneficiary_id`

func (r *Repository) CreateWithdrawal(ctx context.Context, x *dao.Withdrawal) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		INSERT INTO withdrawals (public_id, wallet_id, customer_id, product_id, beneficiary_id, amount, currency,
		                         status, requires_approval, requested_by, provider)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`,
		x.PublicID, x.WalletID, x.CustomerID, x.ProductID, x.BeneficiaryID, x.Amount, x.Currency,
		x.Status, x.RequiresApproval, x.RequestedBy, x.Provider,
	).Scan(&x.ID, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create withdrawal")
	}
	return nil
}

func (r *Repository) UpdateWithdrawal(ctx context.Context, x *dao.Withdrawal) error {
	err := r.executor(ctx).QueryRowContext(ctx, `
		UPDATE withdrawals
		SET status = $2, decided_by = $3, decided_at = $4, decision_note = $5,
		    provider_payout_id = $6, failure_reason = $7, paid_at = $8
		WHERE id = $1 RETURNING updated_at`,
		x.ID, x.Status, x.DecidedBy, x.DecidedAt, x.DecisionNote, x.ProviderPayoutID, x.FailureReason, x.PaidAt,
	).Scan(&x.UpdatedAt)
	if err != nil {
		if isCheckViolation(err, "ck_withdrawals_four_eyes") {
			return apperrors.Wrap(err, apperrors.PermissionDenied, "a withdrawal cannot be approved or rejected by whoever requested it")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to update withdrawal %s", x.PublicID)
	}
	return nil
}

func (r *Repository) GetWithdrawalByPublicID(ctx context.Context, publicID string) (*dao.Withdrawal, error) {
	return r.queryWithdrawal(ctx, `SELECT `+withdrawalColumns+withdrawalFrom+` WHERE x.public_id = $1`, publicID)
}

// LockWithdrawal holds a withdrawal's row until the transaction ends, so an
// approval, a rejection and a provider update cannot interleave.
func (r *Repository) LockWithdrawal(ctx context.Context, id int64) (*dao.Withdrawal, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockWithdrawal must run inside a transaction")
	}
	return r.queryWithdrawal(ctx, `SELECT `+withdrawalColumns+withdrawalFrom+` WHERE x.id = $1 FOR UPDATE OF x`, id)
}

func (r *Repository) GetWithdrawalByProviderRef(ctx context.Context, providerName, providerPayoutID string) (*dao.Withdrawal, error) {
	x, err := scanWithdrawal(r.executor(ctx).QueryRowContext(ctx, `SELECT `+withdrawalColumns+withdrawalFrom+`
		WHERE x.provider = $1 AND x.provider_payout_id = $2`, providerName, providerPayoutID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "no withdrawal for %s payout %q", providerName, providerPayoutID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load withdrawal")
	}
	return x, nil
}

func (r *Repository) queryWithdrawal(ctx context.Context, query string, arg any) (*dao.Withdrawal, error) {
	x, err := scanWithdrawal(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "withdrawal %v not found", arg)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load withdrawal")
	}
	return x, nil
}

// ListWithdrawals lists withdrawals within scope, oldest first — the
// approval queue when filtered to pending_approval. Platform wallets'
// withdrawals are visible only to the whole-estate scope.
func (r *Repository) ListWithdrawals(ctx context.Context, scope *filter.ProductScope, status dao.WithdrawalStatus, customerID *int64, limit int) ([]*dao.Withdrawal, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "x.product_id", arg)
	if err != nil {
		return nil, err
	}
	if scoped != "" {
		where = append(where, scoped)
	}
	if status != "" {
		where = append(where, "x.status = "+arg(status))
	}
	if customerID != nil {
		where = append(where, "x.customer_id = "+arg(*customerID))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	return r.queryWithdrawals(ctx, `SELECT `+withdrawalColumns+withdrawalFrom+clause+` ORDER BY x.created_at LIMIT `+arg(limit), args...)
}

// ListOpenWithdrawals finds withdrawals cleared to send that have not moved
// since before a moment: approved ones to submit again, processing ones
// whose webhook may be lost.
func (r *Repository) ListOpenWithdrawals(ctx context.Context, before time.Time, limit int) ([]*dao.Withdrawal, error) {
	return r.queryWithdrawals(ctx, `SELECT `+withdrawalColumns+withdrawalFrom+`
		WHERE x.status IN ('approved', 'processing') AND x.updated_at < $1 ORDER BY x.updated_at LIMIT $2`, before, limit)
}

func (r *Repository) queryWithdrawals(ctx context.Context, query string, args ...any) ([]*dao.Withdrawal, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list withdrawals")
	}
	defer rows.Close()
	var out []*dao.Withdrawal
	for rows.Next() {
		x, err := scanWithdrawal(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan withdrawal")
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func scanWithdrawal(row rowScanner) (*dao.Withdrawal, error) {
	var x dao.Withdrawal
	err := row.Scan(&x.ID, &x.PublicID, &x.WalletID, &x.CustomerID, &x.ProductID, &x.BeneficiaryID,
		&x.Amount, &x.Currency, &x.Status, &x.RequiresApproval, &x.RequestedBy, &x.DecidedBy, &x.DecidedAt,
		&x.DecisionNote, &x.Provider, &x.ProviderPayoutID, &x.FailureReason, &x.PaidAt,
		&x.WalletPublicID, &x.BeneficiaryPublicID, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &x, nil
}

// WithdrawnSince totals the withdrawals a wallet — or, with walletID nil, a
// customer across all wallets — has made since a moment, counting every one
// not undone (failed, rejected and reversed ones gave the money back).
// Call it with the wallet's ledger lock held, and the customer row locked for
// the customer-wide total, so concurrent requests cannot both fit under a cap.
func (r *Repository) WithdrawnSince(ctx context.Context, customerID int64, walletID *int64, since time.Time) (amount int64, count int64, err error) {
	err = r.executor(ctx).QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0), COUNT(*) FROM withdrawals
		WHERE customer_id = $1 AND ($2::BIGINT IS NULL OR wallet_id = $2) AND created_at >= $3
		  AND status NOT IN ('failed', 'rejected', 'reversed')`, customerID, walletID, since).Scan(&amount, &count)
	if err != nil {
		return 0, 0, apperrors.Wrap(err, apperrors.Internal, "failed to total withdrawals")
	}
	return amount, count, nil
}

// ListBeneficiariesToReseal finds bank accounts not sealed under keyID:
// plaintext stored before encryption existed, or sealed under a key since
// rotated out.
func (r *Repository) ListBeneficiariesToReseal(ctx context.Context, keyID string) ([]*dao.Beneficiary, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `SELECT `+beneficiaryColumns+` FROM beneficiaries
		WHERE account_number IS NOT NULL AND account_number NOT LIKE 'v1:' || $1 || ':%'`, keyID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list beneficiaries to reseal")
	}
	defer rows.Close()
	var out []*dao.Beneficiary
	for rows.Next() {
		b, err := scanBeneficiary(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan beneficiary")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ResealBeneficiaryAccount replaces a stored account number with its form
// under the current key, with a fresh last four and fingerprint. It changes
// the row only if it still holds previous, so two workers resealing at once
// cannot overwrite each other. It reports whether the row changed.
func (r *Repository) ResealBeneficiaryAccount(ctx context.Context, id int64, previous, sealed, last4, fingerprint string) (bool, error) {
	res, err := r.executor(ctx).ExecContext(ctx, `
		UPDATE beneficiaries SET account_number = $3, account_last4 = $4, account_fingerprint = $5
		WHERE id = $1 AND account_number = $2`, id, previous, sealed, last4, fingerprint)
	if err != nil {
		if isUniqueViolation(err) {
			return false, apperrors.Wrap(err, apperrors.AlreadyExists,
				"beneficiary %d is the same account as another of its customer's", id)
		}
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to reseal beneficiary account")
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
