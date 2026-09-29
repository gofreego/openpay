// Package opsmetrics publishes the operational gauges that dashboards and
// alerts are built on: payment success rate per provider, stuck payments,
// webhook backlog and lag, and hold leakage (plan.md Phase 10).
//
// Every figure is read from the database on a schedule rather than counted
// where things happen. A counter bumped inside a transaction that later
// rolls back reports something that never happened; the database only ever
// holds what committed. A minute of latency is a fair price for figures an
// alert can trust.
package opsmetrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/telemetry"

	"github.com/gofreego/goutils/logger"
)

type Repository interface {
	OpsSnapshot(ctx context.Context, openBefore, holdsBefore time.Time) (*dao.OpsSnapshot, error)
	ProviderStats(ctx context.Context, scope *filter.ProductScope, from, to time.Time) ([]*dao.ProviderStats, error)
}

type Config struct {
	// Window is the trailing period provider success rates are measured over.
	Window time.Duration
	// StuckAfter is how old an open payment may be before it counts as
	// stuck: the payment TTL plus room for the expiry job to get to it.
	StuckAfter time.Duration
	// HoldGrace is how far past expiry an active hold may be before it
	// counts as leaked: room for the sweeper's interval.
	HoldGrace time.Duration
}

func (c *Config) WithDefaults() {
	if c.Window <= 0 {
		c.Window = 15 * time.Minute
	}
	if c.StuckAfter <= 0 {
		c.StuckAfter = 40 * time.Minute
	}
	if c.HoldGrace <= 0 {
		c.HoldGrace = 5 * time.Minute
	}
}

// Reading is what one collection saw, returned so it can be checked without
// an OpenTelemetry pipeline.
type Reading struct {
	dao.OpsSnapshot
	// OldestEventAge is how long the oldest unprocessed webhook has waited.
	OldestEventAge time.Duration
	Providers      []*dao.ProviderStats
}

// Collect reads the current figures and records them as gauges.
func Collect(ctx context.Context, repo Repository, cfg Config, now time.Time) (*Reading, error) {
	cfg.WithDefaults()
	snapshot, err := repo.OpsSnapshot(ctx, now.Add(-cfg.StuckAfter), now.Add(-cfg.HoldGrace))
	if err != nil {
		return nil, err
	}
	providers, err := repo.ProviderStats(ctx, filter.AllProducts(), now.Add(-cfg.Window), now)
	if err != nil {
		return nil, err
	}

	r := &Reading{OpsSnapshot: *snapshot, Providers: providers}
	if snapshot.OldestEvent != nil {
		r.OldestEventAge = max(now.Sub(*snapshot.OldestEvent), 0)
	}
	gauges(ctx).record(ctx, r)
	return r, nil
}

// SuccessRateBps is captured over finished attempts, in basis points; ok is
// false when nothing finished, so an idle provider does not read as 0%.
func SuccessRateBps(s *dao.ProviderStats) (rate int64, ok bool) {
	finished := s.Attempts - s.Open
	if finished <= 0 {
		return 0, false
	}
	return s.Captured * 10_000 / finished, true
}

type instruments struct {
	stuck, backlog, overdueHolds, successRate, finished metric.Int64Gauge
	oldestEvent                                         metric.Float64Gauge
}

var (
	once   sync.Once
	shared *instruments
)

func gauges(ctx context.Context) *instruments {
	once.Do(func() {
		meter := telemetry.Meter("ops")
		m := &instruments{}
		int64Gauge := func(name, description string) metric.Int64Gauge {
			g, err := meter.Int64Gauge(name, metric.WithDescription(description))
			if err != nil {
				logger.Error(ctx, "failed to create %s: %v", name, err)
			}
			return g
		}
		m.stuck = int64Gauge("openpay.payments.stuck",
			"Open payments past their expiry that the expiry job has not closed; should be 0")
		m.backlog = int64Gauge("openpay.webhooks.backlog",
			"Stored provider webhooks not yet processed")
		m.overdueHolds = int64Gauge("openpay.holds.overdue",
			"Active holds past expiry the sweeper has not released: customer money locked for nothing")
		m.successRate = int64Gauge("openpay.payments.success_rate_bps",
			"Captured over finished payment attempts in the trailing window, basis points, per provider")
		m.finished = int64Gauge("openpay.payments.finished_attempts",
			"Finished payment attempts in the trailing window, per provider: the sample size behind the success rate")
		var err error
		if m.oldestEvent, err = meter.Float64Gauge("openpay.webhooks.oldest_age",
			metric.WithDescription("How long the oldest unprocessed webhook has waited"), metric.WithUnit("s")); err != nil {
			logger.Error(ctx, "failed to create openpay.webhooks.oldest_age: %v", err)
		}
		shared = m
	})
	return shared
}

func (m *instruments) record(ctx context.Context, r *Reading) {
	set := func(g metric.Int64Gauge, v int64, opts ...metric.RecordOption) {
		if g != nil {
			g.Record(ctx, v, opts...)
		}
	}
	set(m.stuck, r.StuckPayments)
	set(m.backlog, r.EventBacklog)
	set(m.overdueHolds, r.OverdueHolds)
	if m.oldestEvent != nil {
		m.oldestEvent.Record(ctx, r.OldestEventAge.Seconds())
	}
	for _, p := range r.Providers {
		provider := metric.WithAttributes(attribute.String("provider", p.Provider))
		set(m.finished, p.Attempts-p.Open, provider)
		if rate, ok := SuccessRateBps(p); ok {
			set(m.successRate, rate, provider)
		}
	}
}
