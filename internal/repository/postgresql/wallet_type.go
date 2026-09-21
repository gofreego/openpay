package postgresql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// walletTypeColumns reads the owning product's public id through a LEFT JOIN
// (left, because platform-scoped types have no product), so the API can name
// the product without a second query.
const walletTypeColumns = `wt.id, wt.public_id, wt.product_id, p.public_id, wt.scope,
	wt.code, wt.name, wt.currency,
	wt.fundable, wt.grantable, wt.withdrawable, wt.transferable, wt.refundable_to_source,
	wt.allow_negative, wt.expiry_policy, wt.expiry_days,
	wt.max_balance, wt.max_txn_amount, wt.daily_load_limit, wt.status,
	wt.withdrawable_approved_by, wt.withdrawable_approved_at, wt.withdrawable_approval_ref,
	wt.created_at, wt.updated_at`

const walletTypeFrom = ` FROM wallet_types wt LEFT JOIN products p ON p.id = wt.product_id`

func (r *Repository) CreateWalletType(ctx context.Context, walletType *dao.WalletType) error {
	const query = `
		INSERT INTO wallet_types (
			public_id, product_id, scope, code, name, currency,
			fundable, grantable, withdrawable, transferable, refundable_to_source, allow_negative,
			expiry_policy, expiry_days, max_balance, max_txn_amount, daily_load_limit, status,
			withdrawable_approved_by, withdrawable_approved_at, withdrawable_approval_ref
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id, created_at, updated_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		walletType.PublicID, walletType.ProductID, walletType.Scope, walletType.Code,
		walletType.Name, walletType.Currency,
		walletType.Fundable, walletType.Grantable, walletType.Withdrawable,
		walletType.Transferable, walletType.RefundableToSource, walletType.AllowNegative,
		walletType.ExpiryPolicy, walletType.ExpiryDays,
		walletType.MaxBalance, walletType.MaxTxnAmount, walletType.DailyLoadLimit,
		walletType.Status,
		walletType.WithdrawableApprovedBy, walletType.WithdrawableApprovedAt,
		walletType.WithdrawableApprovalRef,
	).Scan(&walletType.ID, &walletType.CreatedAt, &walletType.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists,
				"wallet type %q already exists in this scope", walletType.Code)
		}
		if isForeignKeyViolation(err) {
			return apperrors.Wrap(err, apperrors.NotFound, "product does not exist")
		}
		// A check violation means the capability combination is incoherent.
		// The database is the last line of defence here; the service rejects
		// these with a clearer message first.
		return apperrors.Wrap(err, apperrors.Internal, "failed to create wallet type")
	}
	return nil
}

func (r *Repository) GetWalletTypeByPublicID(ctx context.Context, publicID string) (*dao.WalletType, error) {
	const query = `SELECT ` + walletTypeColumns + walletTypeFrom + ` WHERE wt.public_id = $1`

	walletType, err := scanWalletType(r.executor(ctx).QueryRowContext(ctx, query, publicID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperrors.New(apperrors.NotFound, "wallet type %q not found", publicID)
		}
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load wallet type")
	}
	return walletType, nil
}

// ListWalletTypes returns a product's own types together with the
// platform-scoped ones, because both are spendable within that product and a
// caller asking "what wallets can this customer have here?" needs both.
func (r *Repository) ListWalletTypes(ctx context.Context, productID int64) ([]*dao.WalletType, error) {
	const query = `SELECT ` + walletTypeColumns + walletTypeFrom + `
		WHERE wt.product_id = $1 OR wt.product_id IS NULL
		ORDER BY wt.product_id NULLS LAST, wt.code`

	rows, err := r.executor(ctx).QueryContext(ctx, query, productID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list wallet types")
	}
	defer rows.Close()

	var walletTypes []*dao.WalletType
	for rows.Next() {
		walletType, err := scanWalletType(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan wallet type")
		}
		walletTypes = append(walletTypes, walletType)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate wallet types")
	}
	return walletTypes, nil
}

// UpdateWalletType changes only what stays safe to change once wallets exist.
//
// Absent by design: currency, scope, and every capability flag. A balance
// already held under one set of rules cannot have those rules rewritten
// underneath it — a customer who was told their coins never expire must not
// wake up to an expiry policy. Tightening limits is allowed; the rest requires
// a new type.
func (r *Repository) UpdateWalletType(ctx context.Context, walletType *dao.WalletType) error {
	const query = `
		UPDATE wallet_types
		SET name = $1, status = $2, max_balance = $3, max_txn_amount = $4, daily_load_limit = $5
		WHERE public_id = $6
		RETURNING id`

	var id int64
	err := r.executor(ctx).QueryRowContext(ctx, query,
		walletType.Name, walletType.Status, walletType.MaxBalance,
		walletType.MaxTxnAmount, walletType.DailyLoadLimit, walletType.PublicID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(apperrors.NotFound, "wallet type %q not found", walletType.PublicID)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to update wallet type")
	}

	// Re-read through the join so the caller gets the full row, including the
	// capabilities the update deliberately left alone.
	updated, err := r.GetWalletTypeByPublicID(ctx, walletType.PublicID)
	if err != nil {
		return err
	}
	*walletType = *updated
	return nil
}

func scanWalletType(row rowScanner) (*dao.WalletType, error) {
	var w dao.WalletType
	err := row.Scan(
		&w.ID, &w.PublicID, &w.ProductID, &w.ProductPublicID, &w.Scope, &w.Code, &w.Name, &w.Currency,
		&w.Fundable, &w.Grantable, &w.Withdrawable, &w.Transferable,
		&w.RefundableToSource, &w.AllowNegative,
		&w.ExpiryPolicy, &w.ExpiryDays, &w.MaxBalance, &w.MaxTxnAmount, &w.DailyLoadLimit,
		&w.Status, &w.WithdrawableApprovedBy, &w.WithdrawableApprovedAt, &w.WithdrawableApprovalRef,
		&w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &w, nil
}
