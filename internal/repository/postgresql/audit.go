package postgresql

import (
	"context"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// RecordAudit appends an audit entry.
//
// Run it inside the transaction that makes the change, so the change and its
// record commit together. An audited action whose audit row was lost to a
// separate failed write is worse than no audit at all: it looks complete.
func (r *Repository) RecordAudit(ctx context.Context, entry *dao.AuditEntry) error {
	const query = `
		INSERT INTO audit_log (actor_type, actor_id, action, resource_type, resource_id,
		                       product_id, request_id, before, after)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		entry.ActorType, entry.ActorID, entry.Action, entry.ResourceType, entry.ResourceID,
		entry.ProductID, entry.RequestID, nullableJSON(entry.Before), nullableJSON(entry.After),
	).Scan(&entry.ID, &entry.CreatedAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to record audit entry")
	}
	return nil
}

// nullableJSON keeps empty payloads out of the column as NULL rather than as an
// empty byte string, which JSONB rejects.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
