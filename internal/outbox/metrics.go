package outbox

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gofreego/openpay/internal/telemetry"

	"github.com/gofreego/goutils/logger"
)

// metrics for the drainer. Publish failures and event age are the two signals
// that tell you the outbox is falling behind before anyone notices missing
// events downstream.
type metrics struct {
	published metric.Int64Counter
	failed    metric.Int64Counter
	lag       metric.Float64Histogram
}

func newMetrics(ctx context.Context) *metrics {
	meter := telemetry.Meter("outbox")

	published, err := meter.Int64Counter("openpay.outbox.published",
		metric.WithDescription("Outbox events successfully published"))
	if err != nil {
		logger.Error(ctx, "failed to create outbox published counter: %v", err)
	}

	failed, err := meter.Int64Counter("openpay.outbox.publish_failures",
		metric.WithDescription("Outbox events that could not be published"))
	if err != nil {
		logger.Error(ctx, "failed to create outbox failure counter: %v", err)
	}

	lag, err := meter.Float64Histogram("openpay.outbox.lag",
		metric.WithDescription("Seconds between an event being recorded and published"),
		metric.WithUnit("s"))
	if err != nil {
		logger.Error(ctx, "failed to create outbox lag histogram: %v", err)
	}

	return &metrics{published: published, failed: failed, lag: lag}
}

func (m *metrics) recordPublished(ctx context.Context, topic string, lagSeconds float64) {
	attrs := metric.WithAttributes(attribute.String("topic", topic))
	if m.published != nil {
		m.published.Add(ctx, 1, attrs)
	}
	if m.lag != nil {
		m.lag.Record(ctx, lagSeconds, attrs)
	}
}

func (m *metrics) recordFailure(ctx context.Context, topic string) {
	if m.failed != nil {
		m.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("topic", topic)))
	}
}
