package payment_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// paidTopup returns a captured, applied top-up of amount.
func (f *fixture) paidTopup(w *dao.Wallet, amount int64) *dao.Payment {
	f.t.Helper()
	p, providerID := f.topup(w, amount)
	f.mock.Pay(providerID)
	f.webhook(providerID, "payment.captured")
	f.drain()
	return f.reload(p)
}

func (f *fixture) refund(p *dao.Payment, amount int64) (*dao.Refund, error) {
	return f.engine.CreateRefund(f.ctx, payment.RefundRequest{
		PaymentID: p.ID, Amount: amount, ReasonCode: "customer_request", RequestedBy: "op_1"})
}

func (f *fixture) refundWebhook(r *dao.Refund, eventType string) {
	f.t.Helper()
	h, b := f.mock.RefundWebhook(*r.ProviderRefundID, eventType)
	f.deliver(h, b)
}

func (f *fixture) reloadRefund(r *dao.Refund) *dao.Refund {
	f.t.Helper()
	got, err := f.repo.GetRefundByPublicID(f.ctx, r.PublicID)
	if err != nil {
		f.t.Fatalf("reload refund: %v", err)
	}
	return got
}

// A full refund: the money leaves the wallet at once so it cannot be spent
// while the provider works, and leaves the books when the provider confirms.
func TestFullRefund(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)

	r, err := f.refund(p, 1000)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if r.Status != dao.RefundPending {
		t.Errorf("refund is %s, want pending — refunds are asynchronous", r.Status)
	}
	if got := f.walletBalance(f.wallet); got != 0 {
		t.Errorf("wallet = %d, want 0 — the refunded money must be reserved", got)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 1000 {
		t.Errorf("refunds payable = %d, want 1000 while the provider works", got)
	}

	f.mock.ProcessRefund(*r.ProviderRefundID)
	f.refundWebhook(r, "refund.processed")
	f.drain()

	if got := f.reloadRefund(r); got.Status != dao.RefundProcessed {
		t.Errorf("refund is %s, want processed", got.Status)
	}
	if got := f.reload(p); got.Status != dao.PaymentRefunded {
		t.Errorf("payment is %s, want refunded", got.Status)
	}
	if f.balance(ledger.ProductRefundsPayable("zshala")) != 0 || f.balance(ledger.PSPReceivable(mock.Name)) != 0 {
		t.Errorf("refunds payable %d, receivable %d; want both 0 once the money has left",
			f.balance(ledger.ProductRefundsPayable("zshala")), f.balance(ledger.PSPReceivable(mock.Name)))
	}
	if topics := f.outboxTopics(r.PublicID); fmt.Sprint(topics) != "[refund.processed]" {
		t.Errorf("refund events = %v, want [refund.processed]", topics)
	}
	f.assertLedgerHealthy()
}

func TestPartialRefundsAndOverRefund(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)

	for i, amount := range []int64{300, 700} {
		r, err := f.refund(p, amount)
		if err != nil {
			t.Fatalf("refund %d: %v", i, err)
		}
		f.mock.ProcessRefund(*r.ProviderRefundID)
		if _, err := f.engine.PollRefund(f.ctx, f.reloadRefund(r)); err != nil {
			t.Fatalf("poll refund %d: %v", i, err)
		}
		want := dao.PaymentPartiallyRefunded
		if i == 1 {
			want = dao.PaymentRefunded
		}
		if got := f.reload(p).Status; got != want {
			t.Errorf("after refund %d payment is %s, want %s", i, got, want)
		}
	}

	_, err := f.refund(p, 1)
	if !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("refunding past the capture: error code = %q (%v), want %q", apperrors.CodeOf(err), err, apperrors.FailedPrecondition)
	}
	f.assertLedgerHealthy()
}

// Concurrent refunds cannot together exceed the capture: the database
// constraint on refunded_amount decides, not a check in code.
func TestConcurrentRefundsCannotOverRefund(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	// Top the wallet up further so the wallet balance is not what stops them.
	f.paidTopup(f.wallet, 5000)

	const attempts = 10
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	errs := make([]error, attempts)
	wg.Add(attempts)
	for i := range attempts {
		go func() {
			defer wg.Done()
			<-barrier
			_, errs[i] = f.refund(p, 200)
		}()
	}
	close(barrier)
	wg.Wait()

	var ok int
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !apperrors.Is(err, apperrors.FailedPrecondition) {
			t.Errorf("unexpected failure: %v", err)
		}
	}
	if got := f.reload(p).RefundedAmount; ok != 5 || got != 1000 {
		t.Errorf("%d refunds succeeded, refunded_amount %d; want exactly 5 and 1000", ok, got)
	}
	f.assertLedgerHealthy()
}

// A top-up can only be refunded while its value is still in the wallet:
// refunding money the customer already spent would pay them twice.
func TestSpentTopupCannotBeRefunded(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	if _, err := f.wallets.Spend(f.ctx, walletSpend(f, 800)); err != nil {
		t.Fatalf("spend: %v", err)
	}

	_, err := f.refund(p, 500)
	if !apperrors.Is(err, apperrors.InsufficientBalance) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.InsufficientBalance)
	}
	if got := f.reload(p).RefundedAmount; got != 0 {
		t.Errorf("refunded_amount = %d after a refused refund, want 0 — nothing should persist", got)
	}
	if _, err := f.refund(p, 200); err != nil {
		t.Errorf("refunding what is still there: %v", err)
	}
}

// The provider fails the refund: the money goes back into the wallet,
// reversing the journal that took it, and can be refunded again later.
func TestFailedRefundRestoresTheWallet(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	r, err := f.refund(p, 600)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}

	f.mock.FailRefund(*r.ProviderRefundID, "account closed")
	f.refundWebhook(r, "refund.failed")
	f.drain()

	failed := f.reloadRefund(r)
	if failed.Status != dao.RefundFailed || failed.FailureReason == nil || *failed.FailureReason != "account closed" {
		t.Errorf("refund = %s (%v), want failed with the provider's reason", failed.Status, failed.FailureReason)
	}
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000 restored", got)
	}
	if got := f.reload(p); got.RefundedAmount != 0 || got.Status != dao.PaymentCaptured {
		t.Errorf("payment %s with refunded_amount %d; want captured and 0", got.Status, got.RefundedAmount)
	}
	restore, err := f.repo.GetJournalByExternalID(f.ctx, "refund:"+r.PublicID+":restore")
	if err != nil || restore.ReversesJournalID == nil {
		t.Errorf("restore journal %v (%v), want one reversing the reservation", restore, err)
	}
	if _, err := f.refund(p, 1000); err != nil {
		t.Errorf("refunding again after a failure: %v", err)
	}
	f.assertLedgerHealthy()
}

// A provider timeout leaves the refund initiated with its money reserved;
// the poller submits it again under the same id, and it proceeds.
func TestRefundTimeoutIsRetriedByThePoller(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	f.mock.FailNextRefund(provider.ErrUnavailable("mock: timed out"))

	r, err := f.refund(p, 400)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if r.Status != dao.RefundInitiated || f.walletBalance(f.wallet) != 600 {
		t.Fatalf("refund %s with wallet %d; want initiated with 400 reserved", r.Status, f.walletBalance(f.wallet))
	}

	open, err := f.repo.ListOpenRefunds(f.ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(open) != 1 {
		t.Fatalf("open refunds = %d (%v), want 1", len(open), err)
	}
	polled, err := f.engine.PollRefund(f.ctx, open[0])
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if polled.Status != dao.RefundPending || polled.ProviderRefundID == nil {
		t.Errorf("after poll refund is %s, want pending with a provider id", polled.Status)
	}
	f.assertLedgerHealthy()
}

// A payment its wallet refused is owed back already; refunding it takes the
// money from refunds_payable, never from the wallet.
func TestRefundOfUnappliedPayment(t *testing.T) {
	f := setup(t)
	limit := int64(500)
	capped := f.open(f.walletType(&limit))
	p, providerID := f.topup(capped, 1000)
	f.mock.Pay(providerID)
	f.webhook(providerID, "payment.captured")
	f.drain()

	r, err := f.engine.CreateRefund(f.ctx, payment.RefundRequest{PaymentID: p.ID, Amount: 1000,
		ReasonCode: "unapplied_payment", RequestedBy: "op_1"})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if r.Source != dao.RefundFromUnapplied {
		t.Errorf("source = %s, want unapplied", r.Source)
	}
	f.mock.ProcessRefund(*r.ProviderRefundID)
	if _, err := f.engine.PollRefund(f.ctx, f.reloadRefund(r)); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if f.balance(ledger.ProductRefundsPayable("zshala")) != 0 || f.walletBalance(capped) != 0 {
		t.Errorf("refunds payable %d, wallet %d; want both 0", f.balance(ledger.ProductRefundsPayable("zshala")), f.walletBalance(capped))
	}
	f.assertLedgerHealthy()
}

func TestRefundRefusals(t *testing.T) {
	f := setup(t)
	unpaid, _ := f.topup(f.wallet, 1000)
	_, err := f.refund(unpaid, 100)
	if !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("refunding an unpaid payment: error code = %q", apperrors.CodeOf(err))
	}

	disputed, providerID := f.topup(f.wallet, 1000)
	f.mock.Pay(providerID)
	f.mock.ReportAmount(providerID, 9999)
	f.webhook(providerID, "payment.captured")
	f.drain()
	_, err = f.refund(f.reload(disputed), 100)
	if !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("refunding a payment in suspense: error code = %q", apperrors.CodeOf(err))
	}

	paid := f.paidTopup(f.wallet, 1000)
	_, err = f.engine.CreateRefund(f.ctx, payment.RefundRequest{PaymentID: paid.ID, Amount: 100,
		ReasonCode: "felt like it", RequestedBy: "op_1"})
	if !apperrors.Is(err, apperrors.InvalidArgument) {
		t.Errorf("unknown reason code: error code = %q", apperrors.CodeOf(err))
	}
}

func walletSpend(f *fixture, amount int64) wallet.SpendRequest {
	return wallet.SpendRequest{Wallet: f.wallet, Amount: amount,
		CounterAccountCode: ledger.ProductSales("zshala"), ProductID: f.product.ID,
		Kind: dao.JournalPurchase, ExternalID: fmt.Sprintf("order:spend:%d", amount)}
}
