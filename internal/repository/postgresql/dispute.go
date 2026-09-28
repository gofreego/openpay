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

const disputeColumns = `d.id, d.public_id, d.payment_id, d.product_id, d.provider, d.provider_dispute_id,
	d.amount, d.currency, d.reason, d.status, d.from_wallet, d.from_unapplied, d.from_expense,
	d.evidence_due_by, d.evidence, d.evidence_submitted_by, d.evidence_submitted_at, d.resolved_at,
	p.public_id, d.created_at, d.updated_at`

const disputeFrom = ` FROM disputes d JOIN payments p ON p.id = d.payment_id`

func (r *Repository) CreateDispute(ctx context.Context, d *dao.Dispute) error {
	const insert = `
		INSERT INTO disputes (public_id, payment_id, product_id, provider, provider_dispute_id, amount, currency,
		                      reason, status, from_wallet, from_unapplied, from_expense, evidence_due_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, created_at, updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, insert,
		d.PublicID, d.PaymentID, d.ProductID, d.Provider, d.ProviderDisputeID, d.Amount, d.Currency,
		d.Reason, d.Status, d.FromWallet, d.FromUnapplied, d.FromExpense, d.EvidenceDueBy,
	).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.AlreadyExists, "dispute %s already recorded", d.ProviderDisputeID)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create dispute")
	}
	return nil
}

func (r *Repository) UpdateDispute(ctx context.Context, d *dao.Dispute) error {
	const update = `
		UPDATE disputes
		SET status = $2, evidence = $3, evidence_submitted_by = $4, evidence_submitted_at = $5, resolved_at = $6
		WHERE id = $1
		RETURNING updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, update,
		d.ID, d.Status, d.Evidence, d.EvidenceSubmittedBy, d.EvidenceSubmittedAt, d.ResolvedAt,
	).Scan(&d.UpdatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update dispute %s", d.PublicID)
	}
	return nil
}

func (r *Repository) GetDisputeByPublicID(ctx context.Context, publicID string) (*dao.Dispute, error) {
	return r.queryDispute(ctx, `SELECT `+disputeColumns+disputeFrom+` WHERE d.public_id = $1`, publicID)
}

func (r *Repository) GetDisputeByProviderRef(ctx context.Context, providerName, providerDisputeID string) (*dao.Dispute, error) {
	query := `SELECT ` + disputeColumns + disputeFrom + ` WHERE d.provider = $1 AND d.provider_dispute_id = $2`
	d, err := scanDispute(r.executor(ctx).QueryRowContext(ctx, query, providerName, providerDisputeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "no dispute for %s dispute %q", providerName, providerDisputeID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load dispute")
	}
	return d, nil
}

// LockDispute holds a dispute's row until the transaction ends.
func (r *Repository) LockDispute(ctx context.Context, id int64) (*dao.Dispute, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockDispute must run inside a transaction")
	}
	return r.queryDispute(ctx, `SELECT `+disputeColumns+disputeFrom+` WHERE d.id = $1 FOR UPDATE OF d`, id)
}

func (r *Repository) queryDispute(ctx context.Context, query string, arg any) (*dao.Dispute, error) {
	d, err := scanDispute(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "dispute %v not found", arg)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load dispute")
	}
	return d, nil
}

// ListDisputes is the disputes queue, within scope, oldest evidence deadline
// first — the order someone working the queue needs.
func (r *Repository) ListDisputes(ctx context.Context, scope *filter.ProductScope, status dao.DisputeStatus, limit int) ([]*dao.Dispute, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(scope, "d.product_id", arg)
	if err != nil {
		return nil, err
	}
	if scoped != "" {
		where = append(where, scoped)
	}
	if status != "" {
		where = append(where, "d.status = "+arg(status))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	return r.queryDisputes(ctx, `SELECT `+disputeColumns+disputeFrom+clause+
		` ORDER BY d.evidence_due_by NULLS LAST, d.id LIMIT `+arg(limit), args...)
}

// ListOpenDisputes finds unresolved disputes untouched since before a moment,
// for the poller.
func (r *Repository) ListOpenDisputes(ctx context.Context, before time.Time, limit int) ([]*dao.Dispute, error) {
	return r.queryDisputes(ctx, `SELECT `+disputeColumns+disputeFrom+`
		WHERE d.status IN ('open', 'under_review') AND d.updated_at < $1
		ORDER BY d.updated_at LIMIT $2`, before, limit)
}

func (r *Repository) queryDisputes(ctx context.Context, query string, args ...any) ([]*dao.Dispute, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list disputes")
	}
	defer rows.Close()
	var out []*dao.Dispute
	for rows.Next() {
		d, err := scanDispute(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan dispute")
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate disputes")
	}
	return out, nil
}

func scanDispute(row rowScanner) (*dao.Dispute, error) {
	var d dao.Dispute
	err := row.Scan(&d.ID, &d.PublicID, &d.PaymentID, &d.ProductID, &d.Provider, &d.ProviderDisputeID,
		&d.Amount, &d.Currency, &d.Reason, &d.Status, &d.FromWallet, &d.FromUnapplied, &d.FromExpense,
		&d.EvidenceDueBy, &d.Evidence, &d.EvidenceSubmittedBy, &d.EvidenceSubmittedAt, &d.ResolvedAt,
		&d.PaymentPublicID, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}
