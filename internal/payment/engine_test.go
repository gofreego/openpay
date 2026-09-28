package payment_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/repository/postgresql"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

const webhookPrefix = "/openpay/v1/webhooks/"

type fixture struct {
	t        *testing.T
	ctx      context.Context
	repo     *postgresql.Repository
	mock     *mock.Provider
	engine   *payment.Engine
	webhooks http.Handler
	product  *dao.Product
	customer *dao.Customer
	main     *dao.WalletType
	wallet   *dao.Wallet
	wallets  *wallet.Engine
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: testsupport.Repository(t)}
	f.mock = mock.New("test-secret", "https://mock.test/checkout/")
	registry := provider.NewRegistry([]string{mock.Name}, f.mock)
	f.wallets = wallet.New(f.repo, wallet.Limits{})
	f.engine = payment.New(f.repo, registry, f.wallets, payment.Config{TTL: 30 * time.Minute})
	f.webhooks = payment.WebhookHandler(webhookPrefix, registry, f.repo)

	f.product = &dao.Product{PublicID: ids.New(ids.Product), Code: "zshala", Name: "Zshala",
		Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := f.repo.CreateProduct(f.ctx, f.product); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{mock.Name}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	f.main = f.walletType(nil)
	f.customer = &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: "user-1", Status: dao.CustomerActive}
	if _, err := f.repo.UpsertCustomer(f.ctx, f.customer); err != nil {
		t.Fatalf("customer: %v", err)
	}
	f.wallet = f.open(f.main)
	return f
}

func (f *fixture) walletType(maxBalance *int64) *dao.WalletType {
	f.t.Helper()
	code := "MAIN"
	if maxBalance != nil {
		code = "CAPPED"
	}
	wt := &dao.WalletType{PublicID: ids.New(ids.WalletType), ProductID: &f.product.ID, Scope: dao.WalletScopeProduct,
		Code: code, Name: code, Currency: "INR", Fundable: true, ExpiryPolicy: dao.ExpiryNone,
		MaxBalance: maxBalance, Status: dao.WalletTypeActive}
	if err := f.repo.CreateWalletType(f.ctx, wt); err != nil {
		f.t.Fatalf("wallet type: %v", err)
	}
	return wt
}

func (f *fixture) open(wt *dao.WalletType) *dao.Wallet {
	f.t.Helper()
	w, _, err := f.wallets.Open(f.ctx, f.customer, wt)
	if err != nil {
		f.t.Fatalf("open wallet: %v", err)
	}
	return w
}

func (f *fixture) topup(w *dao.Wallet, amount int64) (*dao.Payment, string) {
	f.t.Helper()
	var p *dao.Payment
	var a *dao.PaymentAttempt
	err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		var err error
		p, a, err = f.engine.CreateTopup(ctx, payment.TopupRequest{ProductID: f.product.ID, Customer: f.customer,
			Wallet: w, Amount: amount, Currency: "INR", Description: "Zshala top-up"})
		return err
	})
	if err != nil {
		f.t.Fatalf("create top-up: %v", err)
	}
	if a.ProviderPaymentID == nil {
		return p, ""
	}
	return p, *a.ProviderPaymentID
}

// deliver posts a webhook to the real handler, as the provider would.
func (f *fixture) deliver(headers http.Header, body []byte) int {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, webhookPrefix+mock.Name, bytes.NewReader(body))
	req.Header = headers
	rec := httptest.NewRecorder()
	f.webhooks.ServeHTTP(rec, req)
	return rec.Code
}

func (f *fixture) webhook(providerID, eventType string) int {
	f.t.Helper()
	h, b := f.mock.Webhook(providerID, eventType)
	return f.deliver(h, b)
}

// drain processes every stored event that is due.
func (f *fixture) drain() {
	f.t.Helper()
	for {
		found, err := f.engine.ProcessNextEvent(f.ctx, f.repo)
		if err != nil {
			f.t.Fatalf("process event: %v", err)
		}
		if !found {
			return
		}
	}
}

func (f *fixture) reload(p *dao.Payment) *dao.Payment {
	f.t.Helper()
	got, err := f.repo.GetPaymentByID(f.ctx, p.ID)
	if err != nil {
		f.t.Fatalf("reload payment: %v", err)
	}
	return got
}

func (f *fixture) balance(code string) int64 {
	f.t.Helper()
	a, err := f.repo.GetLedgerAccountByCode(f.ctx, code)
	if err != nil {
		f.t.Fatalf("account %s: %v", code, err)
	}
	b, _ := f.repo.GetBalance(f.ctx, a.ID)
	return b.Natural(a.Type)
}

func (f *fixture) walletBalance(w *dao.Wallet) int64 {
	f.t.Helper()
	b, _ := f.repo.GetBalance(f.ctx, w.LedgerAccountID)
	return b.Natural(dao.AccountLiability)
}

func (f *fixture) outboxTopics(aggregateID string) []string {
	f.t.Helper()
	var topics []string
	_ = f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		events, err := f.repo.ClaimUnpublishedOutboxEvents(ctx, 100)
		for _, e := range events {
			if e.AggregateID == aggregateID {
				topics = append(topics, e.Topic)
			}
		}
		return err
	})
	return topics
}

func (f *fixture) assertLedgerHealthy() {
	f.t.Helper()
	findings, _, err := f.repo.CheckLedgerInvariants(f.ctx, 50)
	if err != nil {
		f.t.Fatalf("check: %v", err)
	}
	if len(findings) != 0 {
		f.t.Errorf("ledger invariants broken: %v", findings)
	}
}

func wantStatus(t *testing.T, p *dao.Payment, status dao.PaymentStatus, application dao.PaymentApplication) {
	t.Helper()
	if p.Status != status || p.Application != application {
		t.Errorf("payment %s is %s/%s, want %s/%s", p.PublicID, p.Status, p.Application, status, application)
	}
}

// The whole top-up: checkout, payment, webhook, credit, event.
func TestTopupHappyPath(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 50000)
	wantStatus(t, p, dao.PaymentPending, dao.ApplicationPending)

	f.mock.Pay(providerID)
	if code := f.webhook(providerID, "payment.captured"); code != http.StatusOK {
		t.Fatalf("webhook answered %d, want 200", code)
	}
	// The handler only stores: nothing is credited until the processor runs.
	if got := f.walletBalance(f.wallet); got != 0 {
		t.Errorf("wallet = %d before processing, want 0 — the webhook handler must not do ledger work", got)
	}
	f.drain()

	p = f.reload(p)
	wantStatus(t, p, dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 50000 {
		t.Errorf("wallet = %d, want 50000", got)
	}
	if got := f.balance(ledger.PSPReceivable(mock.Name)); got != 50000 {
		t.Errorf("receivable = %d, want 50000", got)
	}
	if topics := f.outboxTopics(p.PublicID); fmt.Sprint(topics) != "[payment.succeeded]" {
		t.Errorf("payment events = %v, want [payment.succeeded]", topics)
	}
	f.assertLedgerHealthy()
}

// Duplicates — the same delivery twice, and two distinct events about the
// same capture — credit once.
func TestDuplicateWebhooksCreditOnce(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)

	h, b := f.mock.Webhook(providerID, "payment.captured")
	f.deliver(h, b)
	f.deliver(h, b) // redelivered verbatim: stored once
	f.webhook(providerID, "payment.captured")
	f.webhook(providerID, "payment.authorized")
	f.drain()

	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000 — credited more than once", got)
	}
	f.assertLedgerHealthy()
}

// Events arriving in the wrong order — captured before pending — converge on
// the provider's actual state, because the event's own claim is never trusted.
func TestOutOfOrderWebhooksConverge(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)

	pendingHeaders, pendingBody := f.mock.Webhook(providerID, "payment.pending")
	f.mock.Pay(providerID)
	f.webhook(providerID, "payment.captured")
	f.drain()
	f.deliver(pendingHeaders, pendingBody) // the stale one turns up late
	f.drain()

	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000", got)
	}
}

// No webhook ever arrives. The poller finds the payment and credits it.
func TestLostWebhookIsRecoveredByPolling(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)

	stale, err := f.repo.ListStalePayments(f.ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale payments = %d (%v), want 1", len(stale), err)
	}
	if _, err := f.engine.Poll(f.ctx, stale[0], "poller"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000", got)
	}
}

// A decline fails the payment with a canonical reason. If the provider later
// reports it captured after all, the money is recorded, not refused.
func TestDeclineThenLateSuccess(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)

	f.mock.Decline(providerID, "insufficient_funds")
	f.webhook(providerID, "payment.failed")
	f.drain()
	failed := f.reload(p)
	wantStatus(t, failed, dao.PaymentFailed, dao.ApplicationPending)
	if failed.FailureCode == nil || *failed.FailureCode != "insufficient_funds" {
		t.Errorf("failure code = %v, want insufficient_funds", failed.FailureCode)
	}

	f.mock.Pay(providerID)
	if _, err := f.engine.Poll(f.ctx, failed, "operator"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000 after the late success", got)
	}
	if topics := f.outboxTopics(p.PublicID); fmt.Sprint(topics) != "[payment.failed payment.succeeded]" {
		t.Errorf("payment events = %v, want failed then succeeded", topics)
	}
	f.assertLedgerHealthy()
}

// The provider reports a different amount than we asked for. Neither figure
// is credited: the money waits in suspense for a person (plan.md D7).
func TestAmountMismatchGoesToSuspense(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)
	f.mock.ReportAmount(providerID, 100000)
	f.webhook(providerID, "payment.captured")
	f.drain()

	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationSuspense)
	if got := f.walletBalance(f.wallet); got != 0 {
		t.Errorf("wallet = %d, want 0 — a disputed amount must not be credited", got)
	}
	if got := f.balance(ledger.PSPSuspense(mock.Name)); got != -100000 {
		t.Errorf("suspense = %d, want the provider's 100000 held there", got)
	}
	f.assertLedgerHealthy()
}

// The wallet reached its limit while the customer was paying. The money came
// in regardless, so it is owed back rather than lost or forced into the wallet.
func TestRefusedCreditIsOwedBack(t *testing.T) {
	f := setup(t)
	limit := int64(500)
	capped := f.open(f.walletType(&limit))
	p, providerID := f.topup(capped, 1000)
	f.mock.Pay(providerID)
	f.webhook(providerID, "payment.captured")
	f.drain()

	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationUnapplied)
	if got := f.walletBalance(capped); got != 0 {
		t.Errorf("wallet = %d, want 0", got)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 1000 {
		t.Errorf("refunds payable = %d, want 1000", got)
	}
	f.assertLedgerHealthy()
}

// A top-up the wallet type forbids is refused before the customer is sent to pay.
func TestUnfundableWalletIsRefusedUpFront(t *testing.T) {
	f := setup(t)
	bonus := &dao.WalletType{PublicID: ids.New(ids.WalletType), ProductID: &f.product.ID, Scope: dao.WalletScopeProduct,
		Code: "BONUS", Name: "Bonus", Currency: "INR", Grantable: true, ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive}
	if err := f.repo.CreateWalletType(f.ctx, bonus); err != nil {
		t.Fatalf("wallet type: %v", err)
	}
	w := f.open(bonus)
	err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		_, _, err := f.engine.CreateTopup(ctx, payment.TopupRequest{ProductID: f.product.ID, Customer: f.customer,
			Wallet: w, Amount: 1000, Currency: "INR"})
		return err
	})
	if !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.WalletOperationDenied)
	}
}

// A provider timeout at creation fails the payment with a reason; nothing
// reached the customer, so nothing can be paid.
func TestProviderTimeoutFailsThePayment(t *testing.T) {
	f := setup(t)
	f.mock.FailNextCreate(provider.ErrUnavailable("mock: timed out"))
	p, providerID := f.topup(f.wallet, 1000)

	if providerID != "" {
		t.Errorf("provider id %q recorded for a create that timed out", providerID)
	}
	failed := f.reload(p)
	wantStatus(t, failed, dao.PaymentFailed, dao.ApplicationPending)
	if failed.FailureCode == nil || *failed.FailureCode != "technical" {
		t.Errorf("failure code = %v, want technical", failed.FailureCode)
	}
}

// An unpaid payment past its expiry is cancelled at the provider and expired
// here. One the customer paid at the last second is credited instead.
func TestExpiry(t *testing.T) {
	f := setup(t)
	unpaid, _ := f.topup(f.wallet, 1000)
	lastSecond, lastSecondID := f.topup(f.wallet, 2000)
	f.mock.Pay(lastSecondID)

	due, err := f.repo.ListExpiredPayments(f.ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("expired payments = %d (%v), want 2", len(due), err)
	}
	for _, p := range due {
		if _, err := f.engine.Expire(f.ctx, p); err != nil {
			t.Fatalf("expire %s: %v", p.PublicID, err)
		}
	}

	wantStatus(t, f.reload(unpaid), dao.PaymentExpired, dao.ApplicationPending)
	wantStatus(t, f.reload(lastSecond), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 2000 {
		t.Errorf("wallet = %d, want 2000", got)
	}
	f.assertLedgerHealthy()
}

// Nothing from an unverified webhook is stored or acted on.
func TestForgedWebhookIsRejected(t *testing.T) {
	f := setup(t)
	_, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)

	headers, body := f.mock.Webhook(providerID, "payment.captured")
	headers.Set(mock.SignatureHeader, "00"+headers.Get(mock.SignatureHeader)[2:])
	if code := f.deliver(headers, body); code != http.StatusUnauthorized {
		t.Errorf("forged webhook answered %d, want 401", code)
	}
	if found, _ := f.engine.ProcessNextEvent(f.ctx, f.repo); found {
		t.Error("a forged webhook was stored")
	}
}

// A webhook that beats its payment's commit is retried later, not dropped.
func TestWebhookForUnknownPaymentIsRetried(t *testing.T) {
	f := setup(t)
	f.webhook("mockpay_never_seen", "payment.captured")
	f.drain()

	var retryAt time.Time
	var attempts int
	testsupport.Query(t, `SELECT next_attempt_at, attempts FROM provider_events WHERE object_id = 'mockpay_never_seen'`,
		&retryAt, &attempts)
	if attempts != 1 || !retryAt.After(time.Now()) {
		t.Errorf("event attempts %d, retry at %s; want 1 attempt and a retry scheduled in the future", attempts, retryAt)
	}
}

// Webhook processing and polling race for the same capture; the payment row
// lock and the journal's external id together keep it to one credit.
func TestConcurrentWebhookAndPollCreditOnce(t *testing.T) {
	f := setup(t)
	p, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)
	for range 5 {
		f.webhook(providerID, "payment.captured")
	}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 5 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := f.engine.ProcessNextEvent(f.ctx, f.repo)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := f.engine.Poll(f.ctx, p, "poller")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent sync: %v", err)
		}
	}
	f.drain()

	wantStatus(t, f.reload(p), dao.PaymentCaptured, dao.ApplicationApplied)
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000", got)
	}
	f.assertLedgerHealthy()
}
