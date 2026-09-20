// Package worker runs OpenPay's background jobs: the outbox drainer today,
// and later the payment status poller, hold expiry sweeper, settlement ingest
// and ledger invariant checker.
//
// It runs alongside the HTTP and gRPC servers via AppNames, and can also be
// deployed on its own so background work does not compete with request traffic.
package worker

import (
	"context"
	"sync"
	"time"

	"github.com/gofreego/openpay/internal/configs"
	"github.com/gofreego/openpay/internal/outbox"
	"github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"

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

	w.done.Add(2)
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
