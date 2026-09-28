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

const paymentColumns = `p.id, p.public_id, p.product_id, p.customer_id, p.purpose, p.wallet_id, p.order_id,
	p.amount, p.currency, p.status, p.application, p.provider, p.captured_amount, p.captured_at,
	p.refunded_amount, p.failure_code, p.failure_reason, p.description, p.return_url, p.expires_at,
	pr.public_id, c.public_id, w.public_id, p.created_at, p.updated_at`

const paymentFrom = ` FROM payments p
	JOIN products pr ON pr.id = p.product_id
	LEFT JOIN customers c ON c.id = p.customer_id
	LEFT JOIN wallets w ON w.id = p.wallet_id`

func (r *Repository) CreatePayment(ctx context.Context, p *dao.Payment) error {
	const insert = `
		INSERT INTO payments (public_id, product_id, customer_id, purpose, wallet_id, order_id, amount, currency,
		                      status, application, provider, description, return_url, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id, created_at, updated_at`
	err := r.executor(ctx).QueryRowContext(ctx, insert,
		p.PublicID, p.ProductID, p.CustomerID, p.Purpose, p.WalletID, p.OrderID, p.Amount, p.Currency,
		p.Status, p.Application, p.Provider, p.Description, p.ReturnURL, p.ExpiresAt,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to create payment")
	}
	return nil
}

func (r *Repository) GetPaymentByPublicID(ctx context.Context, publicID string) (*dao.Payment, error) {
	return r.queryPayment(ctx, `SELECT `+paymentColumns+paymentFrom+` WHERE p.public_id = $1`, publicID)
}

func (r *Repository) GetPaymentByID(ctx context.Context, id int64) (*dao.Payment, error) {
	return r.queryPayment(ctx, `SELECT `+paymentColumns+paymentFrom+` WHERE p.id = $1`, id)
}

// LockPayment loads a payment and holds its row until the transaction ends,
// so two sources — a webhook and the poller — cannot advance it at once.
func (r *Repository) LockPayment(ctx context.Context, id int64) (*dao.Payment, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "LockPayment must run inside a transaction")
	}
	return r.queryPayment(ctx, `SELECT `+paymentColumns+paymentFrom+` WHERE p.id = $1 FOR UPDATE OF p`, id)
}

func (r *Repository) queryPayment(ctx context.Context, query string, arg any) (*dao.Payment, error) {
	p, err := scanPayment(r.executor(ctx).QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "payment %v not found", arg)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load payment")
	}
	return p, nil
}

// UpdatePayment writes the fields that change as a payment progresses.
func (r *Repository) UpdatePayment(ctx context.Context, p *dao.Payment) error {
	const update = `
		UPDATE payments
		SET status = $2, application = $3, provider = $4, captured_amount = $5, captured_at = $6,
		    failure_code = $7, failure_reason = $8, refunded_amount = $9
		WHERE id = $1
		RETURNING updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, update,
		p.ID, p.Status, p.Application, p.Provider, p.CapturedAmount, p.CapturedAt,
		p.FailureCode, p.FailureReason, p.RefundedAmount,
	).Scan(&p.UpdatedAt); err != nil {
		if isCheckViolation(err, "ck_payments_refunded") {
			return apperrors.Wrap(err, apperrors.FailedPrecondition,
				"payment %s cannot be refunded beyond what was captured", p.PublicID)
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to update payment %s", p.PublicID)
	}
	return nil
}

// ListPayments lists payments within scope, newest first.
func (r *Repository) ListPayments(ctx context.Context, f *filter.Payment) ([]*dao.Payment, int64, error) {
	f.WithDefaults()
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	scoped, err := scopeCondition(f.Scope, "p.product_id", arg)
	if err != nil {
		return nil, 0, err
	}
	if scoped != "" {
		where = append(where, scoped)
	}
	if f.ProductID != nil {
		where = append(where, "p.product_id = "+arg(*f.ProductID))
	}
	if f.CustomerID != nil {
		where = append(where, "p.customer_id = "+arg(*f.CustomerID))
	}
	if f.Status != "" {
		where = append(where, "p.status = "+arg(f.Status))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM payments p`+clause, args...).Scan(&total); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to count payments")
	}
	query := `SELECT ` + paymentColumns + paymentFrom + clause +
		` ORDER BY p.id DESC LIMIT ` + arg(f.Limit) + ` OFFSET ` + arg(f.Offset)
	payments, err := r.queryPayments(ctx, query, args...)
	return payments, total, err
}

// ListStalePayments finds open payments with a provider reference that have
// not moved since before a moment — the ones whose webhook may be lost.
func (r *Repository) ListStalePayments(ctx context.Context, before time.Time, limit int) ([]*dao.Payment, error) {
	query := `SELECT ` + paymentColumns + paymentFrom + `
		WHERE p.status IN ('pending', 'authorized') AND p.updated_at < $1
		ORDER BY p.updated_at LIMIT $2`
	return r.queryPayments(ctx, query, before, limit)
}

// ListExpiredPayments finds open payments past their expiry.
func (r *Repository) ListExpiredPayments(ctx context.Context, now time.Time, limit int) ([]*dao.Payment, error) {
	query := `SELECT ` + paymentColumns + paymentFrom + `
		WHERE p.status IN ('created', 'pending', 'authorized') AND p.expires_at <= $1
		ORDER BY p.expires_at LIMIT $2`
	return r.queryPayments(ctx, query, now, limit)
}

func (r *Repository) queryPayments(ctx context.Context, query string, args ...any) ([]*dao.Payment, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list payments")
	}
	defer rows.Close()
	var payments []*dao.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan payment")
		}
		payments = append(payments, p)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate payments")
	}
	return payments, nil
}

func scanPayment(row rowScanner) (*dao.Payment, error) {
	var p dao.Payment
	err := row.Scan(&p.ID, &p.PublicID, &p.ProductID, &p.CustomerID, &p.Purpose, &p.WalletID, &p.OrderID,
		&p.Amount, &p.Currency, &p.Status, &p.Application, &p.Provider, &p.CapturedAmount, &p.CapturedAt,
		&p.RefundedAmount, &p.FailureCode, &p.FailureReason, &p.Description, &p.ReturnURL, &p.ExpiresAt,
		&p.ProductPublicID, &p.CustomerPublicID, &p.WalletPublicID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ---- Attempts ----

const attemptColumns = `id, public_id, payment_id, provider, routing_reason, provider_payment_id,
	status, checkout_url, failure_code, failure_reason, created_at, updated_at`

func (r *Repository) CreatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error {
	const insert = `
		INSERT INTO payment_attempts (public_id, payment_id, provider, routing_reason, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`
	err := r.executor(ctx).QueryRowContext(ctx, insert,
		a.PublicID, a.PaymentID, a.Provider, a.RoutingReason, a.Status,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.Wrap(err, apperrors.FailedPrecondition, "payment already has a live attempt")
		}
		return apperrors.Wrap(err, apperrors.Internal, "failed to create payment attempt")
	}
	return nil
}

func (r *Repository) UpdatePaymentAttempt(ctx context.Context, a *dao.PaymentAttempt) error {
	const update = `
		UPDATE payment_attempts
		SET provider_payment_id = $2, status = $3, checkout_url = $4, failure_code = $5, failure_reason = $6
		WHERE id = $1
		RETURNING updated_at`
	if err := r.executor(ctx).QueryRowContext(ctx, update,
		a.ID, a.ProviderPaymentID, a.Status, a.CheckoutURL, a.FailureCode, a.FailureReason,
	).Scan(&a.UpdatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to update payment attempt %s", a.PublicID)
	}
	return nil
}

// GetAttemptByProviderRef is how a webhook finds its payment.
func (r *Repository) GetAttemptByProviderRef(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error) {
	query := `SELECT ` + attemptColumns + ` FROM payment_attempts WHERE provider = $1 AND provider_payment_id = $2`
	a, err := scanAttempt(r.executor(ctx).QueryRowContext(ctx, query, providerName, providerPaymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperrors.New(apperrors.NotFound, "no attempt for %s payment %q", providerName, providerPaymentID)
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to load payment attempt")
	}
	return a, nil
}

// ListPaymentAttempts returns a payment's attempts, oldest first.
func (r *Repository) ListPaymentAttempts(ctx context.Context, paymentID int64) ([]*dao.PaymentAttempt, error) {
	rows, err := r.executor(ctx).QueryContext(ctx,
		`SELECT `+attemptColumns+` FROM payment_attempts WHERE payment_id = $1 ORDER BY id`, paymentID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list payment attempts")
	}
	defer rows.Close()
	var attempts []*dao.PaymentAttempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan payment attempt")
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate payment attempts")
	}
	return attempts, nil
}

func scanAttempt(row rowScanner) (*dao.PaymentAttempt, error) {
	var a dao.PaymentAttempt
	err := row.Scan(&a.ID, &a.PublicID, &a.PaymentID, &a.Provider, &a.RoutingReason, &a.ProviderPaymentID,
		&a.Status, &a.CheckoutURL, &a.FailureCode, &a.FailureReason, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ---- Transitions ----

func (r *Repository) RecordPaymentTransition(ctx context.Context, t *dao.PaymentTransition) error {
	const insert = `
		INSERT INTO payment_transitions (payment_id, from_status, to_status, source, reference, detail)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`
	if err := r.executor(ctx).QueryRowContext(ctx, insert,
		t.PaymentID, t.From, t.To, t.Source, t.Reference, t.Detail,
	).Scan(&t.ID, &t.CreatedAt); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to record payment transition")
	}
	return nil
}

func (r *Repository) ListPaymentTransitions(ctx context.Context, paymentID int64) ([]*dao.PaymentTransition, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT id, payment_id, from_status, to_status, source, reference, detail, created_at
		FROM payment_transitions WHERE payment_id = $1 ORDER BY id`, paymentID)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list payment transitions")
	}
	defer rows.Close()
	var out []*dao.PaymentTransition
	for rows.Next() {
		var t dao.PaymentTransition
		if err := rows.Scan(&t.ID, &t.PaymentID, &t.From, &t.To, &t.Source, &t.Reference, &t.Detail, &t.CreatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan payment transition")
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate payment transitions")
	}
	return out, nil
}

// ---- Provider events ----

// SaveProviderEvent stores a verified webhook. inserted is false when the
// provider has sent this event before: webhooks are at-least-once, and the
// unique (provider, event_id) is what makes a duplicate harmless.
func (r *Repository) SaveProviderEvent(ctx context.Context, e *dao.ProviderEvent) (inserted bool, err error) {
	const insert = `
		INSERT INTO provider_events (provider, event_id, event_type, object_kind, object_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider, event_id) DO NOTHING
		RETURNING id, received_at`
	err = r.executor(ctx).QueryRowContext(ctx, insert,
		e.Provider, e.EventID, e.EventType, e.ObjectKind, e.ObjectID, e.Payload,
	).Scan(&e.ID, &e.ReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, apperrors.Wrap(err, apperrors.Internal, "failed to store provider event")
	}
	return true, nil
}

// ClaimProviderEvent locks the next unprocessed event that is due, skipping
// any another worker holds. nil when there is nothing to do.
func (r *Repository) ClaimProviderEvent(ctx context.Context, now time.Time) (*dao.ProviderEvent, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal, "ClaimProviderEvent must run inside a transaction")
	}
	const query = `
		SELECT id, provider, event_id, event_type, object_kind, object_id, payload, received_at,
		       processed_at, attempts, next_attempt_at, last_error
		FROM provider_events
		WHERE processed_at IS NULL AND next_attempt_at <= $1
		ORDER BY id
		LIMIT 1
		FOR UPDATE SKIP LOCKED`
	var e dao.ProviderEvent
	err := r.executor(ctx).QueryRowContext(ctx, query, now).Scan(&e.ID, &e.Provider, &e.EventID, &e.EventType,
		&e.ObjectKind, &e.ObjectID, &e.Payload, &e.ReceivedAt, &e.ProcessedAt, &e.Attempts, &e.NextAttemptAt, &e.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to claim provider event")
	}
	return &e, nil
}

func (r *Repository) MarkProviderEventProcessed(ctx context.Context, id int64) error {
	_, err := r.executor(ctx).ExecContext(ctx,
		`UPDATE provider_events SET processed_at = NOW(), attempts = attempts + 1, last_error = NULL WHERE id = $1`, id)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to mark provider event processed")
	}
	return nil
}

// MarkProviderEventFailed records a failed processing attempt and when to try
// again, so a stuck event is visible rather than retried silently forever.
func (r *Repository) MarkProviderEventFailed(ctx context.Context, id int64, cause string, retryAt time.Time) error {
	_, err := r.executor(ctx).ExecContext(ctx,
		`UPDATE provider_events SET attempts = attempts + 1, last_error = $2, next_attempt_at = $3 WHERE id = $1`,
		id, cause, retryAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to mark provider event failed")
	}
	return nil
}

// ---- Request log ----

// RecordProviderRequest logs a provider call on its own connection, never in
// the caller's transaction: a call whose transaction rolled back is exactly
// the one someone will need to see.
func (r *Repository) RecordProviderRequest(ctx context.Context, req *dao.ProviderRequest) error {
	_, err := r.connManager.Primary().ExecContext(ctx, `
		INSERT INTO provider_request_log (provider, operation, payment_id, request, response, error, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		req.Provider, req.Operation, req.PaymentID, nullJSON(req.Request), nullJSON(req.Response), req.Error, req.DurationMs)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to log provider request")
	}
	return nil
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
