package order_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/order"
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
	wallets  *wallet.Engine
	payments *payment.Engine
	orders   *order.Engine
	webhooks http.Handler

	product     *dao.Product
	customer    *dao.Customer
	main, bonus *dao.Wallet
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: testsupport.Repository(t)}
	f.mock = mock.New("test-secret", "https://mock.test/checkout/")
	registry := provider.NewRegistry([]string{mock.Name}, f.mock)
	f.wallets = wallet.New(f.repo, wallet.Limits{})
	f.payments = payment.New(f.repo, registry, f.wallets, payment.Config{})
	f.orders = order.New(f.repo, f.wallets, f.payments, order.Config{})
	f.webhooks = payment.WebhookHandler(webhookPrefix, registry, f.repo)

	f.product = f.newProduct("zshala")
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{mock.Name}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	f.customer = &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: "user-1", Status: dao.CustomerActive}
	if _, err := f.repo.UpsertCustomer(f.ctx, f.customer); err != nil {
		t.Fatalf("customer: %v", err)
	}
	f.main = f.open(f.walletType(f.product, "MAIN", true, false))
	f.bonus = f.open(f.walletType(f.product, "BONUS", false, true))
	return f
}

func (f *fixture) newProduct(code string) *dao.Product {
	f.t.Helper()
	p := &dao.Product{PublicID: ids.New(ids.Product), Code: code, Name: code, Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := f.repo.CreateProduct(f.ctx, p); err != nil {
		f.t.Fatalf("product: %v", err)
	}
	if _, err := ledger.EnsureAccounts(f.ctx, f.repo, ledger.ProductChart(p)); err != nil {
		f.t.Fatalf("product chart: %v", err)
	}
	return p
}

func (f *fixture) walletType(p *dao.Product, code string, fundable, grantable bool) *dao.WalletType {
	f.t.Helper()
	wt := &dao.WalletType{PublicID: ids.New(ids.WalletType), Code: code, Name: code, Currency: "INR",
		Fundable: fundable, Grantable: grantable, ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive,
		Scope: dao.WalletScopePlatform}
	if p != nil {
		wt.ProductID, wt.Scope = &p.ID, dao.WalletScopeProduct
	}
	if err := f.repo.CreateWalletType(f.ctx, wt); err != nil {
		f.t.Fatalf("wallet type: %v", err)
	}
	return wt
}

func (f *fixture) open(wt *dao.WalletType) *dao.Wallet {
	f.t.Helper()
	w, _, err := f.wallets.Open(f.ctx, f.customer, wt)
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	return w
}

// fund puts money in a wallet the way the ledger would record a top-up or grant.
func (f *fixture) fund(w *dao.Wallet, amount int64) {
	f.t.Helper()
	walletType, _ := f.repo.GetWalletTypeByID(f.ctx, w.WalletTypeID)
	var err error
	if walletType.Fundable {
		_, err = f.wallets.Fund(f.ctx, wallet.FundRequest{Wallet: w, Amount: amount, Provider: mock.Name,
			ProductID: f.product.ID, ExternalID: ids.New(ids.Payment)})
	} else {
		_, err = f.wallets.Grant(f.ctx, wallet.GrantRequest{Wallet: w, Amount: amount, FundingProductID: f.product.ID,
			ReasonCode: "promotion", Reference: ids.New(ids.Idempotency)})
	}
	if err != nil {
		f.t.Fatalf("fund: %v", err)
	}
}

// taxed is the plan's example C: ₹300 with ₹46 of it tax.
func taxed(ref string) order.CreateRequest {
	return order.CreateRequest{ExternalRef: ref, Currency: "INR",
		Subtotal: 25400, Tax: 4600, Total: 30000, TaxRate: "18%", BreakdownProvided: true,
		Lines: []order.Line{{Description: "Premium, 1 month", Quantity: 1, UnitAmount: 25400}}}
}

func (f *fixture) create(req order.CreateRequest) (*order.Result, error) {
	req.ProductID = f.product.ID
	if req.Customer == nil {
		req.Customer = f.customer
	}
	var r *order.Result
	err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		var err error
		r, err = f.orders.Create(ctx, req)
		return err
	})
	return r, err
}

func (f *fixture) mustCreate(req order.CreateRequest) *order.Result {
	f.t.Helper()
	r, err := f.create(req)
	if err != nil {
		f.t.Fatalf("create order: %v", err)
	}
	return r
}

func (f *fixture) providerID(p *dao.Payment) string {
	f.t.Helper()
	attempts, err := f.repo.ListPaymentAttempts(f.ctx, p.ID)
	if err != nil || len(attempts) == 0 || attempts[0].ProviderPaymentID == nil {
		f.t.Fatalf("no provider payment for %s (%v)", p.PublicID, err)
	}
	return *attempts[0].ProviderPaymentID
}

func (f *fixture) webhook(providerID, eventType string) {
	f.t.Helper()
	h, b := f.mock.Webhook(providerID, eventType)
	req := httptest.NewRequest(http.MethodPost, webhookPrefix+mock.Name, bytes.NewReader(b))
	req.Header = h
	f.webhooks.ServeHTTP(httptest.NewRecorder(), req)
	for {
		found, err := f.payments.ProcessNextEvent(f.ctx, f.repo)
		if err != nil {
			f.t.Fatalf("process: %v", err)
		}
		if !found {
			return
		}
	}
}

func (f *fixture) reload(o *dao.Order) *dao.Order {
	f.t.Helper()
	got, err := f.repo.GetOrderByPublicID(f.ctx, o.PublicID)
	if err != nil {
		f.t.Fatalf("reload order: %v", err)
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

func (f *fixture) wallet(w *dao.Wallet) (balance, available int64) {
	f.t.Helper()
	b, _ := f.repo.GetBalance(f.ctx, w.LedgerAccountID)
	return b.Natural(dao.AccountLiability), b.Available(dao.AccountLiability)
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

func wantCode(t *testing.T, what string, err error, code apperrors.Code) {
	t.Helper()
	if !apperrors.Is(err, code) {
		t.Errorf("%s: error code = %q (%v), want %q", what, apperrors.CodeOf(err), err, code)
	}
}

// Plan example C: a wallet pays ₹300 of which ₹46 is tax. Revenue, tax and
// the wallet each land in their own account.
func TestWalletOnlyOrderBooksEachComponent(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)

	req := taxed("zs-1001")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 30000}}
	r := f.mustCreate(req)

	if r.Order.Status != dao.OrderPaid || r.Payment != nil {
		t.Errorf("order is %s with payment %v; want paid at once with no card payment", r.Order.Status, r.Payment)
	}
	if b, _ := f.wallet(f.main); b != 70000 {
		t.Errorf("wallet = %d, want 70000", b)
	}
	if got := f.balance(ledger.ProductSales("zshala")); got != 25400 {
		t.Errorf("product sales = %d, want 25400 — the tax must not be booked as revenue", got)
	}
	if got := f.balance(ledger.GSTPayable); got != 4600 {
		t.Errorf("GST payable = %d, want 4600", got)
	}
	f.assertLedgerHealthy()
}

func TestDiscountIsBookedSeparately(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	r := f.mustCreate(order.CreateRequest{ExternalRef: "zs-disc", Currency: "INR",
		Subtotal: 30000, Discount: 5000, Tax: 4500, Total: 29500, BreakdownProvided: true,
		Tenders: []order.WalletTender{{Wallet: f.main, Amount: 29500}}})
	if r.Order.Status != dao.OrderPaid {
		t.Fatalf("order is %s, want paid", r.Order.Status)
	}
	if f.balance(ledger.ProductSales("zshala")) != 30000 || f.balance(ledger.ProductDiscounts("zshala")) != 5000 {
		t.Errorf("sales %d, discounts %d; want the gross 30000 and the 5000 discount apart",
			f.balance(ledger.ProductSales("zshala")), f.balance(ledger.ProductDiscounts("zshala")))
	}
	f.assertLedgerHealthy()
}

// Integrity, not tax logic: amounts that do not add up never reach the ledger.
func TestAmountsMustAddUp(t *testing.T) {
	f := setup(t)
	bad := taxed("zs-bad")
	bad.Tax = 4700
	_, err := f.create(bad)
	wantCode(t, "subtotal + tax != total", err, apperrors.InvalidArgument)

	lines := taxed("zs-lines")
	lines.Lines = []order.Line{{Description: "x", Quantity: 2, UnitAmount: 10000}}
	_, err = f.create(lines)
	wantCode(t, "lines not summing to the subtotal", err, apperrors.InvalidArgument)
}

// A bare total is booked gross — and flagged, so it is visibly incomplete
// rather than quietly tax-free.
func TestBareTotalIsFlagged(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	r := f.mustCreate(order.CreateRequest{ExternalRef: "zs-bare", Currency: "INR",
		Subtotal: 30000, Total: 30000, BreakdownProvided: false,
		Tenders: []order.WalletTender{{Wallet: f.main, Amount: 30000}}})
	if r.Order.TaxBreakdownProvided {
		t.Error("an order sent without a breakdown claims to have one")
	}
	if got := f.balance(ledger.ProductSales("zshala")); got != 30000 {
		t.Errorf("sales = %d, want the gross 30000", got)
	}
}

// Phase 7's exit criterion: ₹200 from the wallet and ₹300 by card settle in
// exactly one balanced journal.
func TestSplitTenderSettlesInOneJournal(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)

	req := order.CreateRequest{ExternalRef: "zs-split", Currency: "INR",
		Subtotal: 42400, Tax: 7600, Total: 50000, BreakdownProvided: true,
		Tenders: []order.WalletTender{{Wallet: f.main, Amount: 20000}}}
	r := f.mustCreate(req)
	if r.Order.Status != dao.OrderPendingPayment || r.Payment == nil || r.Payment.Amount != 30000 {
		t.Fatalf("order %s, payment %v; want pending with a 30000 card payment", r.Order.Status, r.Payment)
	}
	if b, a := f.wallet(f.main); b != 100000 || a != 80000 {
		t.Errorf("wallet %d available %d; want 100000 with 20000 held", b, a)
	}

	id := f.providerID(r.Payment)
	f.mock.Pay(id)
	f.webhook(id, "payment.captured")

	o := f.reload(r.Order)
	if o.Status != dao.OrderPaid {
		t.Fatalf("order is %s, want paid", o.Status)
	}
	journal, err := f.repo.GetJournalByExternalID(f.ctx, "order:"+o.PublicID+":paid")
	if err != nil {
		t.Fatalf("settlement journal: %v", err)
	}
	var debits, credits int64
	for _, p := range journal.Postings {
		if p.Direction == dao.Debit {
			debits += p.Amount
		} else {
			credits += p.Amount
		}
	}
	if debits != 50000 || credits != 50000 || len(journal.Postings) != 4 {
		t.Errorf("journal has %d postings, %d debited and %d credited; want 4 postings balancing at 50000",
			len(journal.Postings), debits, credits)
	}
	if b, a := f.wallet(f.main); b != 80000 || a != 80000 {
		t.Errorf("wallet %d available %d; want 80000 with nothing held", b, a)
	}
	// The card money went straight to the order, not into a wallet first.
	if got := f.balance(ledger.PSPReceivable(mock.Name)); got != 100000+30000 {
		t.Errorf("receivable = %d, want the 100000 top-up plus the 30000 card share", got)
	}
	p, _ := f.repo.GetPaymentByID(f.ctx, r.Payment.ID)
	if p.Application != dao.ApplicationApplied {
		t.Errorf("payment application = %s, want applied", p.Application)
	}
	f.assertLedgerHealthy()
}

// A declined card fails the order and lets go of the wallet share at once.
func TestDeclinedCardReleasesTheHolds(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	req := taxed("zs-decline")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 10000}}
	r := f.mustCreate(req)

	id := f.providerID(r.Payment)
	f.mock.Decline(id, "declined")
	f.webhook(id, "payment.failed")

	if o := f.reload(r.Order); o.Status != dao.OrderFailed {
		t.Errorf("order is %s, want failed", o.Status)
	}
	if b, a := f.wallet(f.main); b != 100000 || a != 100000 {
		t.Errorf("wallet %d available %d; want everything back", b, a)
	}
	f.assertLedgerHealthy()
}

// A provider timeout while opening the card payment fails the order in the
// same transaction: nothing is left held for a sweeper to find.
func TestProviderTimeoutLeavesNoHold(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	f.mock.FailNextCreate(provider.ErrUnavailable("mock: timed out"))
	req := taxed("zs-timeout")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 10000}}
	r := f.mustCreate(req)

	if r.Order.Status != dao.OrderFailed {
		t.Errorf("order is %s, want failed", r.Order.Status)
	}
	if _, a := f.wallet(f.main); a != 100000 {
		t.Errorf("available = %d, want 100000 — a hold was stranded", a)
	}
}

// Granted value is spent before purchased, so promotional credit is used
// rather than left to lapse while the customer's own money pays.
func TestAutoTenderSpendsGrantedValueFirst(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	f.fund(f.bonus, 10000)

	req := taxed("zs-auto")
	req.AutoTender = true
	r := f.mustCreate(req)

	if r.Order.Status != dao.OrderPaid {
		t.Fatalf("order is %s, want paid from wallets", r.Order.Status)
	}
	if b, _ := f.wallet(f.bonus); b != 0 {
		t.Errorf("BONUS = %d, want 0 — granted value goes first", b)
	}
	if b, _ := f.wallet(f.main); b != 80000 {
		t.Errorf("MAIN = %d, want 80000 after paying the remaining 20000", b)
	}
	f.assertLedgerHealthy()
}

// When the wallets cannot cover it all, the card pays the rest.
func TestAutoTenderFallsBackToTheCard(t *testing.T) {
	f := setup(t)
	f.fund(f.bonus, 5000)
	req := taxed("zs-auto-card")
	req.AutoTender = true
	r := f.mustCreate(req)
	if r.Order.GatewayAmount != 25000 || r.Payment == nil {
		t.Errorf("card share = %d, want 25000 with a card payment", r.Order.GatewayAmount)
	}
}

// The card is paid after the order had already failed. The money arrived, so
// it is booked — owed back — and the order stays failed.
func TestLateCardCaptureIsOwedBack(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	req := taxed("zs-late")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 10000}}
	r := f.mustCreate(req)
	id := f.providerID(r.Payment)

	due, err := f.repo.ListExpiredOrders(f.ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("expired orders = %d (%v), want 1", len(due), err)
	}
	if err := f.orders.Expire(f.ctx, due[0]); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if o := f.reload(r.Order); o.Status != dao.OrderFailed {
		t.Fatalf("order is %s after expiry, want failed", o.Status)
	}

	f.mock.Pay(id) // the provider's cancel raced the customer — a real possibility
	p, _ := f.repo.GetPaymentByID(f.ctx, r.Payment.ID)
	if _, err := f.payments.Poll(f.ctx, p, "poller"); err != nil {
		t.Fatalf("poll: %v", err)
	}
	p, _ = f.repo.GetPaymentByID(f.ctx, r.Payment.ID)
	if p.Status != dao.PaymentCaptured || p.Application != dao.ApplicationUnapplied {
		t.Errorf("payment %s/%s; want captured and unapplied", p.Status, p.Application)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 20000 {
		t.Errorf("refunds payable = %d, want the 20000 card share owed back", got)
	}
	if _, a := f.wallet(f.main); a != 100000 {
		t.Errorf("wallet available = %d, want 100000", a)
	}
	f.assertLedgerHealthy()
}

// The wallet share's hold was released and the money spent elsewhere before
// the card completed. The order cannot be settled; it fails, and the card
// money is owed back rather than the wallet going negative.
func TestSpentWalletShareFailsTheOrder(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 10000)
	req := taxed("zs-gone")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 10000}}
	r := f.mustCreate(req)

	if _, err := f.wallets.ReleaseHold(f.ctx, f.main, r.Order.PublicID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := f.wallets.Spend(f.ctx, wallet.SpendRequest{Wallet: f.main, Amount: 10000,
		CounterAccountCode: ledger.ProductSales("zshala"), ProductID: f.product.ID,
		Kind: dao.JournalPurchase, ExternalID: "elsewhere"}); err != nil {
		t.Fatalf("spend: %v", err)
	}

	id := f.providerID(r.Payment)
	f.mock.Pay(id)
	f.webhook(id, "payment.captured")

	if o := f.reload(r.Order); o.Status != dao.OrderFailed {
		t.Errorf("order is %s, want failed", o.Status)
	}
	if b, _ := f.wallet(f.main); b != 0 {
		t.Errorf("wallet = %d, want 0 — never negative", b)
	}
	if got := f.balance(ledger.ProductRefundsPayable("zshala")); got != 20000 {
		t.Errorf("refunds payable = %d, want 20000", got)
	}
	f.assertLedgerHealthy()
}

// A guest pays by card alone; no customer record is needed.
func TestGuestCardOrder(t *testing.T) {
	f := setup(t)
	req := taxed("zs-guest")
	r, err := f.orders.Create(f.ctx, order.CreateRequest{ProductID: f.product.ID, ExternalRef: "zs-guest",
		Currency: "INR", Subtotal: req.Subtotal, Tax: req.Tax, Total: req.Total, BreakdownProvided: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := f.providerID(r.Payment)
	f.mock.Pay(id)
	f.webhook(id, "payment.captured")
	if o := f.reload(r.Order); o.Status != dao.OrderPaid || o.CustomerID != nil {
		t.Errorf("guest order is %s (customer %v), want paid with no customer", o.Status, o.CustomerID)
	}
	f.assertLedgerHealthy()
}

// Orders are idempotent on the product's own reference.
func TestOrderIsIdempotentOnExternalRef(t *testing.T) {
	f := setup(t)
	f.fund(f.main, 100000)
	req := taxed("zs-idem")
	req.Tenders = []order.WalletTender{{Wallet: f.main, Amount: 30000}}
	first := f.mustCreate(req)
	again := f.mustCreate(req)
	if again.Order.PublicID != first.Order.PublicID {
		t.Errorf("retry created order %s, want %s", again.Order.PublicID, first.Order.PublicID)
	}
	if b, _ := f.wallet(f.main); b != 70000 {
		t.Errorf("wallet = %d, want 70000 — charged twice", b)
	}
	changed := req
	changed.Subtotal, changed.Total, changed.Lines = 30400, 35000, nil
	_, err := f.create(changed)
	wantCode(t, "same reference, different total", err, apperrors.AlreadyExists)
}

// One order, one product: a line from another product's catalogue is refused,
// and so is another product's wallet — while a platform wallet may pay.
func TestOrderStaysWithinItsProduct(t *testing.T) {
	f := setup(t)
	other := f.newProduct("bappaapp")
	item := &dao.Item{PublicID: ids.New(ids.Product), ProductID: other.ID, Code: "PRO", Name: "Pro",
		ReferencePrice: 25400, Currency: "INR", Status: "active"}
	if err := f.repo.CreateItem(f.ctx, item); err != nil {
		t.Fatalf("item: %v", err)
	}
	req := taxed("zs-cross")
	req.Lines = []order.Line{{ItemPublicID: item.PublicID, Quantity: 1, UnitAmount: 25400}}
	_, err := f.create(req)
	wantCode(t, "another product's item", err, apperrors.InvalidArgument)

	bappaMain := f.open(f.walletType(other, "MAIN", true, false))
	cross := taxed("zs-cross-wallet")
	cross.Tenders = []order.WalletTender{{Wallet: bappaMain, Amount: 100}}
	_, err = f.create(cross)
	wantCode(t, "another product's wallet", err, apperrors.NotFound)

	points := f.open(f.walletType(nil, "POINTS", false, true))
	f.fund(points, 30000)
	platform := taxed("zs-platform")
	platform.Tenders = []order.WalletTender{{Wallet: points, Amount: 30000}}
	if r, err := f.create(platform); err != nil || r.Order.Status != dao.OrderPaid {
		t.Errorf("platform wallet paying a product order: %v", err)
	}
	f.assertLedgerHealthy()
}
