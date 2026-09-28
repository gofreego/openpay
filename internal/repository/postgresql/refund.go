package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

const refundColumns = `r.id, r.public_id, r.payment_id, r.product_id, r.amount, r.currency, r.status,
	r.source, r.reason_code, r.memo, r.provider, r.provider_refund_id, r.failure_code, r.failure_reason,
	r.requested_by, r.processed_at, p.public_id, r.created_at, r.updated_at`

const refundFrom = ` FROM refunds r JOIN payments p ON p.id = r.payment_id`

func (r *Repository) CreateRefund(ctx context.Context, refund *dao.Refund) error {
	const insert = `
		INSERT INTO refunds (public_id, payment_id, product_id, amount, currency, status, source,
		                     reason_code, memo, provider, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, insert,
		refund.PublicID, refund.PaymentID, refund.ProductID, refund.Amount, refund.Currency, refund.Status,
		refund.Source, refund.ReasonCode, refund.Memo, refund.Provider, refund.RequestedBy,
	).Scan(&refund.ID, &refund.CreatedAt, &refund.UpdatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create refund")
	}
	return nil
}

func (r *Repository) UpdateRefund(ctx context.Context, refund *dao.Refund) error {
	const update = `
		UPDATE refunds
		SET status = $2, provider_refund_id = $3, failure_code = $4, failure_reason = $5, processed_at = $6
		WHERE id = $1
		RETURNING updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, update,
		refund.ID, refund.Status, refund.ProviderRefundID, refund.FailureCode, refund.FailureReason, refund.ProcessedAt,
	).Scan(&refund.UpdatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update refund %s", refund.PublicID)
	}
	return nil
}

func (r *Repository) GetRefundByPublicID(ctx context.Context, publicID string) (*dao.Refund, error) {
	return r.queryRefund(ctx, `SELECT `+refundColumns+refundFrom+` WHERE r.public_id = $1`, publicID)
}

func (r *Repository) GetRefundByProviderRef(ctx context.Context, providerName, providerRefundID string) (*dao.Refund, error) {
	query := `SELECT ` + refundColumns + refundFrom + ` WHERE r.provider = $1 AND r.provider_refund_id = $2`
	refund, err := scanRefund(r.executor(ctx).QueryRowContext(ctx, query, providerName, providerRefundID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "no refund for %s refund %q", providerName, providerRefundID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load refund")
	}
	return refund, nil
}

// LockRefund holds a refund's row until the transaction ends, so a webhook
// and the poller cannot both finish it.
func (r *Repository) LockRefund(ctx context.Context, id int64) (*dao.Refund, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockRefund must run inside a transaction")
	}
	return r.queryRefund(ctx, `SELECT `+refundColumns+refundFrom+` WHERE r.id = $1 FOR UPDATE OF r`, id)
}

func (r *Repository) queryRefund(ctx context.Context, query string, arg any) (*dao.Refund, error) {
	refund, err := scanRefund(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "refund %v not found", arg)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load refund")
	}
	return refund, nil
}

func (r *Repository) ListPaymentRefunds(ctx context.Context, paymentID int64) ([]*dao.Refund, error) {
	return r.queryRefunds(ctx, `SELECT `+refundColumns+refundFrom+` WHERE r.payment_id = $1 ORDER BY r.id`, paymentID)
}

// ListOpenRefunds finds refunds not yet final that have not moved since
// before a moment: initiated ones to ask the provider about again, pending
// ones whose webhook may be lost.
func (r *Repository) ListOpenRefunds(ctx context.Context, before time.Time, limit int) ([]*dao.Refund, error) {
	return r.queryRefunds(ctx, `SELECT `+refundColumns+refundFrom+`
		WHERE r.status IN ('initiated', 'pending') AND r.updated_at < $1
		ORDER BY r.updated_at LIMIT $2`, before, limit)
}

// SumProcessedRefunds is what has actually gone back for a payment.
func (r *Repository) SumProcessedRefunds(ctx context.Context, paymentID int64) (int64, error) {
	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM refunds WHERE payment_id = $1 AND status = 'processed'`,
		paymentID).Scan(&total); err != nil {
		return 0, apperrors.Wrap(err, apperrors.Internal, "failed to total refunds")
	}
	return total, nil
}

func (r *Repository) queryRefunds(ctx context.Context, query string, args ...any) ([]*dao.Refund, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list refunds")
	}
	defer rows.Close()
	var refunds []*dao.Refund
	for rows.Next() {
		refund, err := scanRefund(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan refund")
		}
		refunds = append(refunds, refund)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate refunds")
	}
	return refunds, nil
}

func scanRefund(row rowScanner) (*dao.Refund, error) {
	var r dao.Refund
	err := row.Scan(&r.ID, &r.PublicID, &r.PaymentID, &r.ProductID, &r.Amount, &r.Currency, &r.Status,
		&r.Source, &r.ReasonCode, &r.Memo, &r.Provider, &r.ProviderRefundID, &r.FailureCode, &r.FailureReason,
		&r.RequestedBy, &r.ProcessedAt, &r.PaymentPublicID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
