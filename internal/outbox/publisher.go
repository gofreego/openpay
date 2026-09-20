// Package outbox publishes domain events that were recorded alongside the
// changes that caused them (plan.md D5).
package outbox

import (
	"context"

	"github.com/gofreego/openpay/internal/models/dao"

	"github.com/gofreego/goutils/logger"
)

// Publisher delivers an event to whatever consumers exist.
//
// Delivery is at-least-once: the drainer may publish an event and then fail
// before recording that it did. Consumers must dedupe on EventID.
type Publisher interface {
	Publish(ctx context.Context, event *dao.OutboxEvent) error
	Name() string
}

// LogPublisher writes events to the log. It is a real publisher for local
// development and for the period before consumers exist — every event is still
// recorded durably in the outbox table and marked published in order.
//
// Swap it for a Kafka publisher (goutils/eventqueue) once something consumes.
type LogPublisher struct{}

func (LogPublisher) Name() string { return "log" }

func (LogPublisher) Publish(ctx context.Context, event *dao.OutboxEvent) error {
	logger.Info(ctx, "outbox event published topic=%s aggregate=%s/%s event_id=%s payload=%s",
		event.Topic, event.AggregateType, event.AggregateID, event.EventID, string(event.Payload))
	return nil
}
