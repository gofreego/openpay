package recon

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/telemetry"

	"github.com/gofreego/goutils/logger"
)

// SummaryRepository is what the daily report reads.
type SummaryRepository interface {
	BreakSummary(ctx context.Context, agedBefore time.Time) (open, aged int64, err error)
}

// Summary is the state of reconciliation: the numbers ops and finance watch.
type Summary struct {
	OpenBreaks int64
	AgedBreaks int64
	// Suspense and Receivable are per provider, natural balances. Suspense
	// should trend to zero; a receivable that only grows means settlements
	// are not arriving.
	Suspense   map[string]int64
	Receivable map[string]int64
}

// AlertConfig says when reconciliation needs a person.
type AlertConfig struct {
	// AgedAfter is how long a break may stay open before it is aged.
	AgedAfter time.Duration `yaml:"AgedAfter"`
	// SuspenseThreshold is how much, either way, a provider's suspense may
	// hold before it alerts. Minor units.
	SuspenseThreshold int64 `yaml:"SuspenseThreshold"`
}

func (c *AlertConfig) WithDefaults() {
	if c.AgedAfter <= 0 {
		c.AgedAfter = 48 * time.Hour
	}
}

// Cycle is one reconciliation run: ingest every provider's new settlements,
// flag what they have not settled, then report — and alert on aged breaks
// or suspense drift. The worker runs it on a schedule.
func (e *Engine) Cycle(ctx context.Context, summaries SummaryRepository, alerts AlertConfig) (*Summary, error) {
	alerts.WithDefaults()
	for _, name := range e.providers.Names() {
		n, err := e.Ingest(ctx, name)
		if err != nil {
			logger.Error(ctx, "failed to ingest %s settlements: %v", name, err)
		} else if n > 0 {
			logger.Info(ctx, "ingested %d %s settlements", n, name)
		}
		if _, err := e.FlagUnsettled(ctx, name); err != nil {
			logger.Error(ctx, "failed to flag unsettled %s payments: %v", name, err)
		}
	}

	s, err := e.Summarize(ctx, summaries, alerts.AgedAfter)
	if err != nil {
		return nil, err
	}
	reconMetrics(ctx).record(ctx, s)

	if s.AgedBreaks > 0 {
		logger.Error(ctx, "RECON: %d of %d open breaks are older than %s", s.AgedBreaks, s.OpenBreaks, alerts.AgedAfter)
	}
	for name, balance := range s.Suspense {
		if abs(balance) > alerts.SuspenseThreshold {
			logger.Error(ctx, "RECON: %s suspense holds %d, above the %d threshold", name, balance, alerts.SuspenseThreshold)
		}
	}
	return s, nil
}

// Summarize reports open and aged breaks and each provider's suspense and
// receivable balances.
func (e *Engine) Summarize(ctx context.Context, summaries SummaryRepository, agedAfter time.Duration) (*Summary, error) {
	open, aged, err := summaries.BreakSummary(ctx, e.now().Add(-agedAfter))
	if err != nil {
		return nil, err
	}
	s := &Summary{OpenBreaks: open, AgedBreaks: aged, Suspense: map[string]int64{}, Receivable: map[string]int64{}}
	for _, name := range e.providers.Names() {
		if s.Suspense[name], err = e.naturalBalance(ctx, ledger.PSPSuspense(name)); err != nil {
			return nil, err
		}
		if s.Receivable[name], err = e.naturalBalance(ctx, ledger.PSPReceivable(name)); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (e *Engine) naturalBalance(ctx context.Context, code string) (int64, error) {
	account, err := e.repo.GetLedgerAccountByCode(ctx, code)
	if err != nil {
		return 0, err
	}
	b, err := e.repo.GetBalance(ctx, account.ID)
	if err != nil {
		return 0, err
	}
	return b.Natural(account.Type), nil
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type metrics struct {
	openBreaks metric.Int64Gauge
	agedBreaks metric.Int64Gauge
	suspense   metric.Int64Gauge
}

var (
	metricsOnce   sync.Once
	sharedMetrics *metrics
)

// reconMetrics exposes what to alert on: aged breaks above zero, and a
// suspense balance that does not trend back to zero.
func reconMetrics(ctx context.Context) *metrics {
	metricsOnce.Do(func() {
		meter := telemetry.Meter("recon")
		m := &metrics{}
		var err error
		if m.openBreaks, err = meter.Int64Gauge("openpay.recon.open_breaks",
			metric.WithDescription("Reconciliation breaks awaiting a person")); err != nil {
			logger.Error(ctx, "failed to create open breaks gauge: %v", err)
		}
		if m.agedBreaks, err = meter.Int64Gauge("openpay.recon.aged_breaks",
			metric.WithDescription("Open reconciliation breaks past their age threshold")); err != nil {
			logger.Error(ctx, "failed to create aged breaks gauge: %v", err)
		}
		if m.suspense, err = meter.Int64Gauge("openpay.recon.suspense",
			metric.WithDescription("Provider suspense balance, minor units; should trend to zero")); err != nil {
			logger.Error(ctx, "failed to create suspense gauge: %v", err)
		}
		sharedMetrics = m
	})
	return sharedMetrics
}

func (m *metrics) record(ctx context.Context, s *Summary) {
	if m.openBreaks != nil {
		m.openBreaks.Record(ctx, s.OpenBreaks)
	}
	if m.agedBreaks != nil {
		m.agedBreaks.Record(ctx, s.AgedBreaks)
	}
	if m.suspense != nil {
		for name, balance := range s.Suspense {
			m.suspense.Record(ctx, balance, metric.WithAttributes(attribute.String("provider", name)))
		}
	}
}
