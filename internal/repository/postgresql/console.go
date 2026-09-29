package postgresql

import (
	"context"
	"strconv"
	"strings"

	"github.com/lib/pq"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Read paths for the admin console: listings across entities, the audit
// log, and the raw provider traffic behind a payment.

type conditions struct {
	where []string
	args  []any
}

func (c *conditions) arg(v any) string {
	c.args = append(c.args, v)
	return "$" + strconv.Itoa(len(c.args))
}

func (c *conditions) add(clause string) { c.where = append(c.where, clause) }

func (c *conditions) scope(scope *filter.ProductScope, column string) error {
	s, err := scopeCondition(scope, column, c.arg)
	if err != nil {
		return err
	}
	if s != "" {
		c.add(s)
	}
	return nil
}

func (c *conditions) clause() string {
	if len(c.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(c.where, " AND ")
}

// ListOrders returns a page of orders, newest first, and how many match.
func (r *Repository) ListOrders(ctx context.Context, f *filter.Order) ([]*dao.Order, int64, error) {
	f.WithDefaults()
	var c conditions
	if err := c.scope(f.Scope, "o.product_id"); err != nil {
		return nil, 0, err
	}
	if f.ProductID != nil {
		c.add("o.product_id = " + c.arg(*f.ProductID))
	}
	if f.CustomerID != nil {
		c.add("o.customer_id = " + c.arg(*f.CustomerID))
	}
	if f.Status != "" {
		c.add("o.status = " + c.arg(f.Status))
	}
	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM orders o`+c.clause(), c.args...).Scan(&total); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to count orders")
	}
	query := `SELECT ` + orderColumns + orderFrom + c.clause() +
		` ORDER BY o.id DESC LIMIT ` + c.arg(f.Limit) + ` OFFSET ` + c.arg(f.Offset)
	rows, err := r.executor(ctx).QueryContext(ctx, query, c.args...)
	if err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to list orders")
	}
	defer rows.Close()
	var orders []*dao.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to scan order")
		}
		orders = append(orders, o)
	}
	return orders, total, rows.Err()
}

// SearchRefunds returns a page of refunds across payments, newest first.
func (r *Repository) SearchRefunds(ctx context.Context, f *filter.Refund) ([]*dao.Refund, int64, error) {
	f.WithDefaults()
	var c conditions
	if err := c.scope(f.Scope, "r.product_id"); err != nil {
		return nil, 0, err
	}
	if f.ProductID != nil {
		c.add("r.product_id = " + c.arg(*f.ProductID))
	}
	if f.Status != "" {
		c.add("r.status = " + c.arg(f.Status))
	}
	var total int64
	if err := r.executor(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM refunds r`+c.clause(), c.args...).Scan(&total); err != nil {
		return nil, 0, apperrors.Wrap(err, apperrors.Internal, "failed to count refunds")
	}
	refunds, err := r.queryRefunds(ctx, `SELECT `+refundColumns+refundFrom+c.clause()+
		` ORDER BY r.id DESC LIMIT `+c.arg(f.Limit)+` OFFSET `+c.arg(f.Offset), c.args...)
	return refunds, total, err
}

// ListAuditLog returns entries newest first, before BeforeID when set.
func (r *Repository) ListAuditLog(ctx context.Context, f *filter.Audit) ([]*dao.AuditEntry, error) {
	f.WithDefaults()
	var c conditions
	if f.Scope == nil {
		return nil, apperrors.New(apperrors.Internal, "audit log read without a scope")
	}
	if !f.Scope.All() {
		// Platform entries (no product) are central-only.
		c.add("a.product_id = ANY(" + c.arg(pq.Array(f.Scope.IDs())) + ")")
	}
	if f.ResourceType != "" {
		c.add("a.resource_type = " + c.arg(f.ResourceType))
	}
	if f.ResourceID != "" {
		c.add("a.resource_id = " + c.arg(f.ResourceID))
	}
	if f.ActorID != "" {
		c.add("a.actor_id = " + c.arg(f.ActorID))
	}
	if f.BeforeID > 0 {
		c.add("a.id < " + c.arg(f.BeforeID))
	}
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT a.id, a.actor_type, a.actor_id, a.action, a.resource_type, a.resource_id, a.product_id, p.public_id,
		       COALESCE(a.request_id, ''), a.before, a.after, a.created_at
		FROM audit_log a LEFT JOIN products p ON p.id = a.product_id`+c.clause()+`
		ORDER BY a.id DESC LIMIT `+c.arg(f.Limit), c.args...)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list audit log")
	}
	defer rows.Close()
	var out []*dao.AuditEntry
	for rows.Next() {
		var e dao.AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.Action, &e.ResourceType, &e.ResourceID,
			&e.ProductID, &e.ProductPublicID, &e.RequestID, &e.Before, &e.After, &e.CreatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan audit entry")
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ListProviderRequests returns request-log rows about any of refs, oldest first.
func (r *Repository) ListProviderRequests(ctx context.Context, refs []string) ([]*dao.ProviderRequestRecord, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT provider, operation, COALESCE(payment_id, ''), COALESCE(request::text, ''), COALESCE(response::text, ''),
		       COALESCE(error, ''), duration_ms, created_at
		FROM provider_request_log WHERE payment_id = ANY($1) ORDER BY id LIMIT 500`, pq.Array(refs))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list provider requests")
	}
	defer rows.Close()
	var out []*dao.ProviderRequestRecord
	for rows.Next() {
		var x dao.ProviderRequestRecord
		if err := rows.Scan(&x.Provider, &x.Operation, &x.Reference, &x.Request, &x.Response, &x.Error,
			&x.DurationMs, &x.CreatedAt); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan provider request")
		}
		out = append(out, &x)
	}
	return out, rows.Err()
}

// ListProviderEventsFor returns webhooks from provider about any of the
// provider object ids, oldest first.
func (r *Repository) ListProviderEventsFor(ctx context.Context, provider string, objectIDs []string) ([]*dao.ProviderEvent, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `
		SELECT id, provider, event_id, event_type, object_kind, object_id, payload, received_at,
		       processed_at, attempts, next_attempt_at, last_error
		FROM provider_events WHERE provider = $1 AND object_id = ANY($2) ORDER BY id LIMIT 500`,
		provider, pq.Array(objectIDs))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to list provider events")
	}
	defer rows.Close()
	var out []*dao.ProviderEvent
	for rows.Next() {
		var e dao.ProviderEvent
		if err := rows.Scan(&e.ID, &e.Provider, &e.EventID, &e.EventType, &e.ObjectKind, &e.ObjectID, &e.Payload,
			&e.ReceivedAt, &e.ProcessedAt, &e.Attempts, &e.NextAttemptAt, &e.LastError); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan provider event")
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
