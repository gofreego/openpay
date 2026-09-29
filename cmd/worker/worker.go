// Package worker runs OpenPay's background jobs: the outbox drainer, the
// idempotency sweeper, the chart-of-accounts bootstrap and the ledger
// invariant checker, and the hold and wallet expiry sweepers; later the
// payment status poller and settlement ingest.
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
	"github.com/gofreego/openpay/internal/order"
	"github.com/gofreego/openpay/internal/outbox"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/recon"
	"github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/internal/withdrawal"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/fieldcrypt"

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

	engine := wallet.New(repo, w.cfg.Service.Wallet)
	w.cfg.Service.Payments.WithDefaults()
	registry, _ := payment.Providers(w.cfg.Service.Payments)
	payments := payment.New(repo, registry, engine, w.cfg.Service.Payments)
	// The worker processes webhooks too, so its payment engine must know about
	// orders: an order's card capture settles the order in the same transaction.
	orders := order.New(repo, engine, payments, w.cfg.Service.Orders)
	reconciler := recon.New(repo, registry, payments, w.cfg.Service.Recon)
	// Handles payout webhooks the worker processes, and polls quiet payouts.
	cipher, err := fieldcrypt.New(w.cfg.Service.Encryption)
	if err != nil {
		logger.Panic(ctx, "field encryption is not configured (Service.Encryption): %v", err)
	}
	withdrawals := withdrawal.New(repo, registry, engine, payments, cipher, w.cfg.Service.Withdrawals)
	// Brings every account number under the current key: plaintext from
	// before encryption, and anything sealed under a key since rotated out.
	n, duplicates, err := withdrawals.ResealAccounts(ctx, repo)
	if err != nil {
		logger.Error(ctx, "failed to reseal beneficiary accounts under key %s: %v", cipher.CurrentKeyID(), err)
	} else if n > 0 {
		logger.Info(ctx, "resealed %d bank account numbers under key %s", n, cipher.CurrentKeyID())
	}
	if len(duplicates) > 0 {
		logger.Error(ctx, "beneficiaries %v duplicate another account of the same customer and were left under their old key; merge them", duplicates)
	}

	w.done.Add(14)
	go func() {
		defer w.done.Done()
		w.every(ctx, "withdrawal poller", w.cfg.Worker.PaymentPollInterval, func() { w.pollWithdrawals(ctx, repo, withdrawals) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "reconciliation", w.cfg.Worker.ReconInterval, func() {
			if _, err := reconciler.Cycle(ctx, repo, w.cfg.Service.ReconAlerts); err != nil {
				logger.Error(ctx, "reconciliation cycle failed: %v", err)
			}
		})
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "order expiry sweeper", w.cfg.Worker.PaymentPollInterval, func() { w.expireOrders(ctx, repo, orders) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "dispute poller", w.cfg.Worker.PaymentPollInterval, func() { w.pollDisputes(ctx, repo, payments) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "refund poller", w.cfg.Worker.PaymentPollInterval, func() { w.pollRefunds(ctx, repo, payments) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "payment event processor", w.cfg.Worker.PaymentEventInterval, func() { w.processPaymentEvents(ctx, repo, payments) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "payment poller", w.cfg.Worker.PaymentPollInterval, func() { w.pollPayments(ctx, repo, payments) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "payment expiry sweeper", w.cfg.Worker.PaymentPollInterval, func() { w.expirePayments(ctx, repo, payments) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "hold expiry sweeper", w.cfg.Worker.HoldSweepInterval, func() { w.expireHolds(ctx, repo) })
	}()
	go func() {
		defer w.done.Done()
		w.every(ctx, "wallet expiry sweeper", w.cfg.Worker.WalletExpiryInterval, func() { w.expireWallets(ctx, repo, engine) })
	}()
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

// every runs job now and then on each tick until ctx ends.
func (w *Worker) every(ctx context.Context, name string, interval time.Duration, job func()) {
	logger.Info(ctx, "%s started: interval=%s", name, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		job()
		select {
		case <-ctx.Done():
			logger.Info(ctx, "%s stopped", name)
			return
		case <-ticker.C:
		}
	}
}

// sweepBatch bounds one pass of a sweeper, so a backlog is worked through
// over a few ticks rather than in one enormous burst.
const sweepBatch = 500

// processPaymentEvents drains stored webhooks until none are due.
func (w *Worker) processPaymentEvents(ctx context.Context, repo service.Repository, payments *payment.Engine) {
	for range sweepBatch {
		found, err := payments.ProcessNextEvent(ctx, repo)
		if err != nil {
			logger.Error(ctx, "failed to process payment event: %v", err)
			return
		}
		if !found {
			return
		}
	}
}

// pollPayments asks providers about open payments that have gone quiet.
// Webhooks will be missed; this is what makes that harmless.
func (w *Worker) pollPayments(ctx context.Context, repo service.Repository, payments *payment.Engine) {
	stale, err := repo.ListStalePayments(ctx, time.Now().Add(-w.cfg.Service.Payments.PollAfter), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list stale payments: %v", err)
		return
	}
	for _, p := range stale {
		if _, err := payments.Poll(ctx, p, "poller"); err != nil {
			logger.Warn(ctx, "failed to poll payment %s: %v", p.PublicID, err)
		}
	}
}

// pollRefunds recovers refunds that went quiet: resubmits ones a timeout left
// initiated, and fetches pending ones whose webhook never came.
func (w *Worker) pollRefunds(ctx context.Context, repo service.Repository, payments *payment.Engine) {
	open, err := repo.ListOpenRefunds(ctx, time.Now().Add(-w.cfg.Service.Payments.PollAfter), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list open refunds: %v", err)
		return
	}
	for _, r := range open {
		if _, err := payments.PollRefund(ctx, r); err != nil {
			logger.Warn(ctx, "failed to poll refund %s: %v", r.PublicID, err)
		}
	}
}

// pollDisputes fetches unresolved disputes whose webhook may have been lost.
// A missed "lost" would leave contested money held forever.
func (w *Worker) pollDisputes(ctx context.Context, repo service.Repository, payments *payment.Engine) {
	open, err := repo.ListOpenDisputes(ctx, time.Now().Add(-w.cfg.Service.Payments.PollAfter), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list open disputes: %v", err)
		return
	}
	for _, d := range open {
		if _, err := payments.PollDispute(ctx, d); err != nil {
			logger.Warn(ctx, "failed to poll dispute %s: %v", d.PublicID, err)
		}
	}
}

// expireOrders resolves orders still awaiting their card share past expiry,
// through their payment — which asks the provider first — so held wallet
// shares are released for an unpaid order and settled for a late-paid one.
func (w *Worker) expireOrders(ctx context.Context, repo service.Repository, orders *order.Engine) {
	due, err := repo.ListExpiredOrders(ctx, time.Now(), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list expired orders: %v", err)
		return
	}
	for _, o := range due {
		if err := orders.Expire(ctx, o); err != nil {
			logger.Warn(ctx, "failed to expire order %s: %v", o.PublicID, err)
		}
	}
}

// pollWithdrawals recovers payouts that went quiet: resubmits ones a timeout
// left approved, and fetches processing ones whose webhook never came. A
// missed bounce would leave a customer's money nowhere.
func (w *Worker) pollWithdrawals(ctx context.Context, repo service.Repository, withdrawals *withdrawal.Engine) {
	open, err := repo.ListOpenWithdrawals(ctx, time.Now().Add(-w.cfg.Service.Payments.PollAfter), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list open withdrawals: %v", err)
		return
	}
	for _, x := range open {
		if _, err := withdrawals.Poll(ctx, x); err != nil {
			logger.Warn(ctx, "failed to poll withdrawal %s: %v", x.PublicID, err)
		}
	}
}

// expirePayments closes payments nobody completed in time, after checking
// with the provider that they really are unpaid.
func (w *Worker) expirePayments(ctx context.Context, repo service.Repository, payments *payment.Engine) {
	due, err := repo.ListExpiredPayments(ctx, time.Now(), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list expired payments: %v", err)
		return
	}
	for _, p := range due {
		if _, err := payments.Expire(ctx, p); err != nil {
			logger.Warn(ctx, "failed to expire payment %s: %v", p.PublicID, err)
		}
	}
}

// expireHolds releases holds past their expiry, one transaction each, so one
// bad hold cannot block the rest. A stranded hold is customer money nobody
// can spend (plan.md phase 7), which is why this runs every minute.
func (w *Worker) expireHolds(ctx context.Context, repo service.Repository) {
	due, err := repo.ListExpiredHolds(ctx, time.Now(), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list expired holds: %v", err)
		return
	}
	var expired int
	for _, externalID := range due {
		err := repo.WithTx(ctx, func(ctx context.Context) error {
			_, err := repo.ExpireHold(ctx, externalID)
			return err
		})
		if err != nil {
			// Captured or released in the meantime is the benign case.
			if !apperrors.Is(err, apperrors.FailedPrecondition) {
				logger.Error(ctx, "failed to expire hold %s: %v", externalID, err)
			}
			continue
		}
		expired++
	}
	if expired > 0 {
		logger.Info(ctx, "expired %d holds", expired)
	}
}

// expireWallets lapses dormant rolling-expiry wallets. A wallet that moved
// since it was found is left alone (ErrExpiryStale) until the next pass.
func (w *Worker) expireWallets(ctx context.Context, repo service.Repository, engine *wallet.Engine) {
	candidates, err := repo.ListRollingExpiryCandidates(ctx, time.Now(), sweepBatch)
	if err != nil {
		logger.Error(ctx, "failed to list expiring wallets: %v", err)
		return
	}
	var expired int
	for _, candidate := range candidates {
		if _, err := engine.Expire(ctx, candidate); err != nil {
			if !apperrors.Is(err, apperrors.FailedPrecondition) {
				logger.Error(ctx, "failed to expire wallet %s: %v", candidate.WalletPublicID, err)
			}
			continue
		}
		expired++
	}
	if expired > 0 {
		logger.Info(ctx, "expired %d dormant wallets", expired)
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
