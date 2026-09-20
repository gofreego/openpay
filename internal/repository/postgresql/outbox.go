package postgresql

import (
	"context"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// SaveOutboxEvent records an event. It must run inside the transaction that
// makes the change the event describes, so the two commit or fail together.
func (r *Repository) SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error {
	const query = `
		INSERT INTO outbox_events (event_id, topic, aggregate_type, aggregate_id, payload)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`

	err := r.executor(ctx).QueryRowContext(ctx, query,
		event.EventID, event.Topic, event.AggregateType, event.AggregateID, event.Payload,
	).Scan(&event.ID, &event.CreatedAt)
	if err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to save outbox event")
	}
	return nil
}

// ClaimUnpublishedOutboxEvents locks a batch for this worker.
//
// FOR UPDATE SKIP LOCKED is what allows more than one drainer to run: each
// takes rows nobody else holds instead of queueing behind them, so no event is
// published twice and no worker blocks. The locks are held by the surrounding
// transaction, which is why this must run inside one.
func (r *Repository) ClaimUnpublishedOutboxEvents(ctx context.Context, limit int) ([]*dao.OutboxEvent, error) {
	if !InTx(ctx) {
		return nil, apperrors.New(apperrors.Internal,
			"ClaimUnpublishedOutboxEvents must run inside a transaction, otherwise the row locks are released immediately")
	}

	const query = `
		SELECT id, event_id, topic, aggregate_type, aggregate_id, payload,
		       created_at, published_at, attempts, COALESCE(last_error, '')
		FROM outbox_events
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := r.executor(ctx).QueryContext(ctx, query, limit)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to claim outbox events")
	}
	defer rows.Close()

	var events []*dao.OutboxEvent
	for rows.Next() {
		var e dao.OutboxEvent
		if err := rows.Scan(&e.ID, &e.EventID, &e.Topic, &e.AggregateType, &e.AggregateID,
			&e.Payload, &e.CreatedAt, &e.PublishedAt, &e.Attempts, &e.LastError); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan outbox event")
		}
		events = append(events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to iterate outbox events")
	}
	return events, nil
}

func (r *Repository) MarkOutboxEventPublished(ctx context.Context, id int64) error {
	const query = `
		UPDATE outbox_events
		SET published_at = NOW(), attempts = attempts + 1, last_error = NULL
		WHERE id = $1`

	if _, err := r.executor(ctx).ExecContext(ctx, query, id); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to mark outbox event published")
	}
	return nil
}

// MarkOutboxEventFailed counts the attempt and keeps the reason, so an event
// stuck behind a permanent failure is visible instead of retrying in silence.
func (r *Repository) MarkOutboxEventFailed(ctx context.Context, id int64, cause string) error {
	const query = `UPDATE outbox_events SET attempts = attempts + 1, last_error = $2 WHERE id = $1`

	if _, err := r.executor(ctx).ExecContext(ctx, query, id, cause); err != nil {
		return apperrors.Wrap(err, apperrors.Internal, "failed to mark outbox event failed")
	}
	return nil
}
