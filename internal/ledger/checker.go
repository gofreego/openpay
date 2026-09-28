package ledger

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/telemetry"
	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

// findingsPerInvariant bounds a run's report. A ledger with more broken rows
// than this in one invariant needs investigating, not a longer list.
const findingsPerInvariant = 50

// CheckRepository is what running the invariant checks needs.
type CheckRepository interface {
	CheckLedgerInvariants(ctx context.Context, limit int) ([]dao.LedgerCheckFinding, bool, error)
	RecordLedgerCheckRun(ctx context.Context, run *dao.LedgerCheckRun) error
}

// RunCheck checks every ledger invariant, records the run, and alerts on drift.
//
// It never repairs anything. A drifted balance could be the balance that is
// wrong or the postings that are wrong, and choosing between them is a
// judgement about what money actually did — which is a person's job, made
// with a reversing journal, never a script's (plan.md D3).
//
// A run that fails to complete is recorded as an error rather than dropped, so
// "the checker has not run" is as visible as "the checker found drift".
func RunCheck(ctx context.Context, repo CheckRepository, trigger dao.LedgerCheckTrigger, triggeredBy *string) (*dao.LedgerCheckRun, error) {
	run := &dao.LedgerCheckRun{
		PublicID:    ids.New(ids.LedgerCheckRun),
		Trigger:     trigger,
		TriggeredBy: triggeredBy,
		StartedAt:   time.Now(),
	}

	findings, truncated, checkErr := repo.CheckLedgerInvariants(ctx, findingsPerInvariant)
	run.FinishedAt = time.Now()
	run.Findings, run.Truncated = findings, truncated

	switch {
	case checkErr != nil:
		run.Status = dao.LedgerCheckError
		msg := checkErr.Error()
		run.Error = &msg
		logger.Error(ctx, "ledger invariant check could not complete: %v", checkErr)
	case len(findings) > 0:
		run.Status = dao.LedgerCheckDrift
		// Loud on purpose: every finding on its own line, so each one is
		// searchable and none hides in a summary.
		logger.Error(ctx, "LEDGER DRIFT: %d invariant violations found (truncated=%t)", len(findings), truncated)
		for _, f := range findings {
			logger.Error(ctx, "LEDGER DRIFT: %s on %q: expected %d, actual %d %s",
				f.Invariant, f.Subject, f.Expected, f.Actual, f.Detail)
		}
	default:
		run.Status = dao.LedgerCheckOK
		logger.Info(ctx, "ledger invariant check passed in %s", run.FinishedAt.Sub(run.StartedAt))
	}

	checkMetrics(ctx).record(ctx, run)

	// Recorded outside any transaction: the check ran against a read-only
	// snapshot, and its result must be stored even when it is bad news.
	if err := repo.RecordLedgerCheckRun(ctx, run); err != nil {
		return run, err
	}
	return run, nil
}

type metrics struct {
	runs     metric.Int64Counter
	findings metric.Int64Gauge
}

var (
	metricsOnce   sync.Once
	sharedMetrics *metrics
)

// checkMetrics exposes two signals to alert on: findings above zero, and no
// successful run recently (absence of openpay.ledger.check.runs with
// status=ok).
func checkMetrics(ctx context.Context) *metrics {
	metricsOnce.Do(func() {
		meter := telemetry.Meter("ledger")
		m := &metrics{}
		var err error
		if m.runs, err = meter.Int64Counter("openpay.ledger.check.runs",
			metric.WithDescription("Ledger invariant check runs, by status")); err != nil {
			logger.Error(ctx, "failed to create ledger check run counter: %v", err)
		}
		if m.findings, err = meter.Int64Gauge("openpay.ledger.check.findings",
			metric.WithDescription("Invariant violations found by the latest ledger check; anything above zero is an incident")); err != nil {
			logger.Error(ctx, "failed to create ledger findings gauge: %v", err)
		}
		sharedMetrics = m
	})
	return sharedMetrics
}

func (m *metrics) record(ctx context.Context, run *dao.LedgerCheckRun) {
	if m.runs != nil {
		m.runs.Add(ctx, 1, metric.WithAttributes(
			attribute.String("status", string(run.Status)),
			attribute.String("trigger", string(run.Trigger))))
	}
	// An errored run found nothing because it checked nothing; leave the
	// gauge at the last real answer rather than reporting a false zero.
	if m.findings != nil && run.Status != dao.LedgerCheckError {
		m.findings.Record(ctx, int64(len(run.Findings)))
	}
}
