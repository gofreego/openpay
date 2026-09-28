package payment_test

import (
	"fmt"
	"testing"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// openDispute has the customer's bank charge a payment back and delivers the
// webhook, as the provider would.
func (f *fixture) openDispute(p *dao.Payment, amount int64) (*dao.Dispute, string) {
	f.t.Helper()
	attempts, err := f.repo.ListPaymentAttempts(f.ctx, p.ID)
	if err != nil {
		f.t.Fatalf("attempts: %v", err)
	}
	id := f.mock.OpenDispute(*attempts[0].ProviderPaymentID, amount, "fraud")
	h, b := f.mock.DisputeWebhook(id, "dispute.created")
	f.deliver(h, b)
	f.drain()
	d, err := f.repo.GetDisputeByProviderRef(f.ctx, mock.Name, id)
	if err != nil {
		f.t.Fatalf("dispute not recorded: %v", err)
	}
	return d, id
}

func (f *fixture) resolveDispute(providerDisputeID string, won bool) *dao.Dispute {
	f.t.Helper()
	f.mock.ResolveDispute(providerDisputeID, won)
	h, b := f.mock.DisputeWebhook(providerDisputeID, "dispute.closed")
	f.deliver(h, b)
	f.drain()
	d, err := f.repo.GetDisputeByProviderRef(f.ctx, mock.Name, providerDisputeID)
	if err != nil {
		f.t.Fatalf("dispute: %v", err)
	}
	return d
}

// The contested money is no longer the customer's to spend while the bank
// decides; when the dispute is lost, the PSP keeps it.
func TestDisputeLost(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)

	d, providerID := f.openDispute(p, 1000)
	if d.FromWallet != 1000 || f.walletBalance(f.wallet) != 0 {
		t.Errorf("from wallet %d, wallet %d; want 1000 held and 0 spendable", d.FromWallet, f.walletBalance(f.wallet))
	}
	if got := f.balance(ledger.ProductDisputed("zshala")); got != 1000 {
		t.Errorf("disputed = %d, want 1000", got)
	}
	if got := f.reload(p).Status; got != dao.PaymentDisputed {
		t.Errorf("payment is %s, want disputed", got)
	}
	// While the bank decides, a refund would return the money twice.
	if _, err := f.refund(f.reload(p), 100); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("refund during a dispute: error code = %q", apperrors.CodeOf(err))
	}

	if _, err := f.engine.SubmitDisputeEvidence(f.ctx, d.ID, "delivery receipt #4411", "op_1"); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	lost := f.resolveDispute(providerID, false)
	if lost.Status != dao.DisputeLost || f.reload(p).Status != dao.PaymentDisputeLost {
		t.Errorf("dispute %s, payment %s; want lost and dispute_lost", lost.Status, f.reload(p).Status)
	}
	if f.balance(ledger.ProductDisputed("zshala")) != 0 || f.balance(ledger.PSPReceivable(mock.Name)) != 0 {
		t.Errorf("disputed %d, receivable %d; want both 0 once the PSP has kept it",
			f.balance(ledger.ProductDisputed("zshala")), f.balance(ledger.PSPReceivable(mock.Name)))
	}
	if topics := f.outboxTopics(d.PublicID); fmt.Sprint(topics) != "[dispute.opened dispute.lost]" {
		t.Errorf("dispute events = %v", topics)
	}
	f.assertLedgerHealthy()
}

// A won dispute puts the money back exactly where it was taken from.
func TestDisputeWonRestoresTheWallet(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	_, providerID := f.openDispute(p, 1000)

	won := f.resolveDispute(providerID, true)
	if won.Status != dao.DisputeWon || f.reload(p).Status != dao.PaymentDisputeWon {
		t.Errorf("dispute %s, payment %s; want won and dispute_won", won.Status, f.reload(p).Status)
	}
	if got := f.walletBalance(f.wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000 back", got)
	}
	f.assertLedgerHealthy()
}

// The customer spent most of the top-up before charging it back. What is left
// in the wallet is held; the rest is our loss unless the dispute is won.
func TestDisputeAfterTheMoneyWasSpent(t *testing.T) {
	for _, won := range []bool{false, true} {
		t.Run(fmt.Sprintf("won=%t", won), func(t *testing.T) {
			f := setup(t)
			p := f.paidTopup(f.wallet, 1000)
			if _, err := f.wallets.Spend(f.ctx, walletSpend(f, 700)); err != nil {
				t.Fatalf("spend: %v", err)
			}

			d, providerID := f.openDispute(p, 1000)
			if d.FromWallet != 300 || d.FromExpense != 700 {
				t.Errorf("from wallet %d, from expense %d; want 300 and 700", d.FromWallet, d.FromExpense)
			}
			f.resolveDispute(providerID, won)

			wantWallet, wantLoss := int64(0), int64(700)
			if won {
				wantWallet, wantLoss = 300, 0
			}
			if got := f.walletBalance(f.wallet); got != wantWallet {
				t.Errorf("wallet = %d, want %d", got, wantWallet)
			}
			if got := f.balance(ledger.ProductChargebacks("zshala")); got != wantLoss {
				t.Errorf("chargeback losses = %d, want %d", got, wantLoss)
			}
			f.assertLedgerHealthy()
		})
	}
}

// A payment already refunded in full can still be charged back — the bank
// does not ask first. Nothing is left to hold, so all of it is at risk, and
// the refund is what wins it.
func TestDisputeAfterAFullRefund(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	r, err := f.refund(p, 1000)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	f.mock.ProcessRefund(*r.ProviderRefundID)
	if _, err := f.engine.PollRefund(f.ctx, f.reloadRefund(r)); err != nil {
		t.Fatalf("poll refund: %v", err)
	}

	d, providerID := f.openDispute(f.reload(p), 1000)
	if d.FromWallet != 0 || d.FromExpense != 1000 {
		t.Errorf("from wallet %d, from expense %d; want 0 and 1000 — the refunded money is not the customer's to hold twice",
			d.FromWallet, d.FromExpense)
	}
	f.resolveDispute(providerID, true)
	if got := f.balance(ledger.ProductChargebacks("zshala")); got != 0 {
		t.Errorf("chargeback losses = %d after winning, want 0", got)
	}
	f.assertLedgerHealthy()
}

// Duplicate dispute webhooks record one dispute and hold the money once.
func TestDuplicateDisputeWebhooks(t *testing.T) {
	f := setup(t)
	p := f.paidTopup(f.wallet, 1000)
	d, providerID := f.openDispute(p, 400)
	for range 3 {
		h, b := f.mock.DisputeWebhook(providerID, "dispute.updated")
		f.deliver(h, b)
	}
	f.drain()

	if got := f.balance(ledger.ProductDisputed("zshala")); got != 400 {
		t.Errorf("disputed = %d, want 400 held once", got)
	}
	if got := f.walletBalance(f.wallet); got != 600 {
		t.Errorf("wallet = %d, want 600", got)
	}
	if again, _ := f.repo.GetDisputeByProviderRef(f.ctx, mock.Name, providerID); again.PublicID != d.PublicID {
		t.Error("a duplicate webhook recorded a second dispute")
	}
	f.assertLedgerHealthy()
}
