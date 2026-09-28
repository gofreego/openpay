package order_test

import (
	"testing"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/order"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// paidSplit is an order of ₹500 (₹76 tax) paid ₹200 from MAIN and ₹300 by card.
func (f *fixture) paidSplit(ref string) *dao.Order {
	f.t.Helper()
	f.fund(f.main, 20000)
	r := f.mustCreate(order.CreateRequest{ExternalRef: ref, Currency: "INR",
		Subtotal: 42400, Tax: 7600, Total: 50000, BreakdownProvided: true,
		Tenders: []order.WalletTender{{Wallet: f.main, Amount: 20000}}})
	id := f.providerID(r.Payment)
	f.mock.Pay(id)
	f.webhook(id, "payment.captured")
	o := f.reload(r.Order)
	if o.Status != dao.OrderPaid {
		f.t.Fatalf("order is %s, want paid", o.Status)
	}
	return o
}

func (f *fixture) refund(o *dao.Order, amount int64, b *order.Breakdown, destination string) (*dao.OrderRefund, []*dao.OrderRefundPart, error) {
	return f.orders.Refund(f.ctx, order.RefundRequest{OrderID: o.ID, Amount: amount, Breakdown: b,
		Destination: destination, ReasonCode: "customer_request", RequestedBy: "test"})
}

// A full refund to source: the card share goes back to the card, the wallet
// share to the wallet, and every component the order booked is reversed.
func TestFullOrderRefundToSource(t *testing.T) {
	f := setup(t)
	o := f.paidSplit("zs-r1")

	_, parts, err := f.refund(o, 50000, &order.Breakdown{Subtotal: 42400, Tax: 7600}, "")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if len(parts) != 2 || parts[0].RefundID == nil || parts[0].Amount != 30000 || parts[1].WalletID == nil {
		t.Errorf("parts = %+v, want the 30000 card share first, then the wallet", parts)
	}
	if b, _ := f.wallet(f.main); b != 20000 {
		t.Errorf("MAIN = %d, want its 20000 back", b)
	}
	if f.balance(ledger.ProductSales("zshala")) != 0 || f.balance(ledger.GSTPayable) != 0 {
		t.Errorf("sales %d, GST %d; want both reversed to 0", f.balance(ledger.ProductSales("zshala")), f.balance(ledger.GSTPayable))
	}
	if got := f.reload(o).Status; got != dao.OrderRefunded {
		t.Errorf("order is %s, want refunded", got)
	}

	// The card share leaves the books when the provider confirms.
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 30000 {
		t.Errorf("refunds payable = %d, want 30000 until the card refund is processed", got)
	}
	cardRefund, err := f.repo.GetRefundByPublicID(f.ctx, *parts[0].RefundPublicID)
	if err != nil {
		t.Fatalf("card refund: %v", err)
	}
	if cardRefund.Source != dao.RefundFromOrder {
		t.Errorf("card refund source = %s, want order", cardRefund.Source)
	}
	f.mock.ProcessRefund(*cardRefund.ProviderRefundID)
	if _, err := f.payments.PollRefund(f.ctx, cardRefund); err != nil {
		t.Fatalf("poll card refund: %v", err)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 0 {
		t.Errorf("refunds payable = %d, want 0 once processed", got)
	}
	f.assertLedgerHealthy()
}

// Partial refunds carry their own tax split, and together cannot exceed any
// component the order charged — the database refuses it.
func TestPartialOrderRefundsAndOverRefund(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	req := taxed("zs-r2")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 30000}}
	o := f.mustCreate(req).Order

	if _, _, err := f.refund(o, 10000, &order.Breakdown{Subtotal: 8000, Tax: 2000}, ""); err != nil {
		t.Fatalf("first refund: %v", err)
	}
	if got := f.reload(o).Status; got != dao.OrderPartiallyRefunded {
		t.Errorf("order is %s, want partially_refunded", got)
	}
	// Only 2600 of tax is left; this asks for 3000.
	_, _, err := f.refund(f.reload(o), 10000, &order.Breakdown{Subtotal: 7000, Tax: 3000}, "")
	wantCode(t, "refunding more tax than was charged", err, apperrors.FailedPrecondition)
	_, _, err = f.refund(f.reload(o), 10000, &order.Breakdown{Subtotal: 8000, Tax: 1000}, "")
	wantCode(t, "a breakdown not adding up", err, apperrors.InvalidArgument)
	f.assertLedgerHealthy()
}

// With no breakdown sent, the split is proportional to the order's — and the
// refund says so, so finance can see which refunds were split for them.
func TestRefundWithoutBreakdownIsSplitAndFlagged(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	req := taxed("zs-r3")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 30000}}
	o := f.mustCreate(req).Order

	x, _, err := f.refund(o, 10000, nil, "")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if x.TaxBreakdownProvided || x.Tax != 1533 || x.Subtotal != 8467 {
		t.Errorf("refund split subtotal %d tax %d (provided %t); want 8467 + 1533, flagged",
			x.Subtotal, x.Tax, x.TaxBreakdownProvided)
	}
	f.assertLedgerHealthy()
}

// Destination "wallet": the card share becomes store credit instead of going
// back to the card — no provider refund at all.
func TestRefundToWalletAsStoreCredit(t *testing.T) {
	f := setup(t)
	o := f.paidSplit("zs-r4")
	if b, _ := f.wallet(f.main); b != 0 {
		t.Fatalf("MAIN = %d before the refund, want 0", b)
	}

	_, parts, err := f.refund(o, 50000, &order.Breakdown{Subtotal: 42400, Tax: 7600}, order.DestinationWallet)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	for _, p := range parts {
		if p.RefundID != nil {
			t.Error("a store-credit refund started a card refund")
		}
	}
	if b, _ := f.wallet(f.main); b != 50000 {
		t.Errorf("MAIN = %d, want all 50000 as store credit", b)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 0 {
		t.Errorf("refunds payable = %d, want 0", got)
	}
	f.assertLedgerHealthy()
}

// Real money is returned first; promotional credit last.
func TestRefundReturnsRealMoneyFirst(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 20000)
	f.fund(f.bonus, 10000)
	req := taxed("zs-r5")
	req.Tenders = []order.WalletTender{{Wallet: f.bonus, Amount: 10000}, {Wallet: f.main, Amount: 20000}}
	o := f.mustCreate(req).Order

	if _, _, err := f.refund(o, 15000, nil, ""); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if b, _ := f.wallet(f.main); b != 15000 {
		t.Errorf("MAIN = %d, want 15000 back", b)
	}
	if b, _ := f.wallet(f.bonus); b != 0 {
		t.Errorf("BONUS = %d, want 0 — granted value is returned last", b)
	}
	f.assertLedgerHealthy()
}

func TestOrderRefundRefusals(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 5000)
	pending := taxed("zs-r6")
	pending.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 5000}}
	o := f.mustCreate(pending).Order
	_, _, err := f.refund(o, 100, nil, "")
	wantCode(t, "refunding an unpaid order", err, apperrors.FailedPrecondition)

	guest, err := f.orders.Create(f.ctx, order.CreateRequest{ProductID: f.product.ID, ExternalRef: "zs-r7",
		Currency: "INR", Subtotal: 1000, Total: 1000})
	if err != nil {
		t.Fatalf("guest order: %v", err)
	}
	id := f.providerID(guest.Payment)
	f.mock.Pay(id)
	f.webhook(id, "payment.captured")
	_, _, err = f.refund(f.reload(guest.Order), 1000, nil, order.DestinationWallet)
	wantCode(t, "store credit for a guest", err, apperrors.FailedPrecondition)
}
