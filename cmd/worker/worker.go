// Package worker runs OpenPay's background jobs: the outbox drainer, the
// idempotency sweeper, the chart-of-accounts bootstrap and the ledger
// invariant checker; later the payment status poller, hold expiry sweeper and
// settlement ingest.
//
// It runs alongside the HTTP and gRPC servers via AppNames, and can also be
// deployed on its own so background work does not compete with request traffic.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/gofreego/openpay/internal/configs"
	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/outbox"
	"github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/pkg/apperrors"

	"github.com/gofreego/goutils/logger"
)

type Worker struct {
	cfg    *configs.Configuration
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func NewWorker(cfg *configs.Configuration) *Worker {
	return &Worker{cfg: cfg}
}

func (w *Worker) Name() string { return "Worker" }

func (w *Worker) Run(ctx context.Context) error {
	w.cfg.Worker.WithDefaults()

	repo := repository.GetInstance(ctx, &w.cfg.Repository)

	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	drainer := outbox.NewDrainer(w.cfg.Worker.Outbox, repo, outbox.LogPublisher{})

	w.done.Add(4)
	go func() {
		defer w.done.Done()
		w.checkLedger(ctx, repo)
	}()
	go func() {
		defer w.done.Done()
		w.ensureChart(ctx, repo)
	}()
	go func() {
		defer w.done.Done()
		drainer.Run(ctx)
	}()
	go func() {
		defer w.done.Done()
		w.sweepIdempotencyKeys(ctx, repo)
	}()

	<-ctx.Done()
	return nil
}

func (w *Worker) Shutdown(ctx context.Context) {
	if w.cancel != nil {
		w.cancel()
	}
	w.done.Wait()
	logger.Info(ctx, "worker stopped")
}

// ensureChart creates any missing chart-of-accounts entries: the platform
// accounts, one set per configured provider and bank, and those of products
// registered before their chart was created at registration.
//
// It retries rather than giving up, because a brief database outage at startup
// should delay the chart, not leave it half-built until the next deploy. An
// account that exists but contradicts the chart is not retried: that is a
// finding for a person, and retrying would only repeat it.
func (w *Worker) ensureChart(ctx context.Context, repo service.Repository) {
	const retryEvery = 30 * time.Second
	for {
		created, err := ledger.EnsureChart(ctx, repo, w.cfg.Ledger)
		if err == nil {
			logger.Info(ctx, "chart of accounts up to date: %d accounts created", created)
			return
		}
		if apperrors.Is(err, apperrors.FailedPrecondition) {
			logger.Error(ctx, "chart of accounts conflicts with the ledger, not retrying: %v", err)
			return
		}
		logger.Error(ctx, "failed to ensure chart of accounts, retrying in %s: %v", retryEvery, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(retryEvery):
		}
	}
}

// checkLedger runs the ledger invariant checks at startup and then on an
// interval. Failures are recorded and logged by ledger.RunCheck; this loop
// only keeps it running.
func (w *Worker) checkLedger(ctx context.Context, repo service.Repository) {
	interval := w.cfg.Worker.LedgerCheckInterval
	logger.Info(ctx, "ledger invariant checker started: interval=%s", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := ledger.RunCheck(ctx, repo, dao.LedgerCheckScheduled, nil); err != nil {
			logger.Error(ctx, "failed to record ledger check run: %v", err)
		}
		select {
		case <-ctx.Done():
			logger.Info(ctx, "ledger invariant checker stopped")
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) sweepIdempotencyKeys(ctx context.Context, repo service.Repository) {
	interval := w.cfg.Worker.IdempotencySweepInterval
	logger.Info(ctx, "idempotency sweeper started: interval=%s batch=%d",
		interval, w.cfg.Worker.IdempotencySweepBatch)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info(ctx, "idempotency sweeper stopped")
			return
		case <-ticker.C:
			deleted, err := repo.DeleteExpiredIdempotencyKeys(ctx, w.cfg.Worker.IdempotencySweepBatch)
			if err != nil {
				logger.Error(ctx, "failed to sweep expired idempotency keys: %v", err)
				continue
			}
			if deleted > 0 {
				logger.Info(ctx, "swept %d expired idempotency keys", deleted)
			}
		}
	}
}
