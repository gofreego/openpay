package recon_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/models/filter"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/recon"
	"github.com/gofreego/openpay/internal/repository/postgresql"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/internal/wallet"
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
	recon    *recon.Engine
	webhooks http.Handler
	customer *dao.Customer
	products map[string]*dao.Product
	mains    map[string]*dao.Wallet
}

func setup(t *testing.T, cfg recon.Config) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: testsupport.Repository(t),
		products: map[string]*dao.Product{}, mains: map[string]*dao.Wallet{}}
	f.mock = mock.New("test-secret", "https://mock.test/checkout/")
	registry := provider.NewRegistry([]string{mock.Name}, f.mock)
	f.wallets = wallet.New(f.repo, wallet.Limits{})
	f.payments = payment.New(f.repo, registry, f.wallets, payment.Config{})
	f.recon = recon.New(f.repo, registry, f.payments, cfg)
	f.webhooks = payment.WebhookHandler(webhookPrefix, registry, f.repo)

	f.customer = &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: "user-1", Status: dao.CustomerActive}
	if _, err := f.repo.UpsertCustomer(f.ctx, f.customer); err != nil {
		t.Fatalf("customer: %v", err)
	}
	for _, code := range []string{"zshala", "bappaapp"} {
		p := &dao.Product{PublicID: ids.New(ids.Product), Code: code, Name: code, Status: dao.ProductActive, DefaultCurrency: "INR"}
		if err := f.repo.CreateProduct(f.ctx, p); err != nil {
			t.Fatalf("product: %v", err)
		}
		f.products[code] = p
		wt := &dao.WalletType{PublicID: ids.New(ids.WalletType), ProductID: &p.ID, Scope: dao.WalletScopeProduct,
			Code: "MAIN", Name: "Main", Currency: "INR", Fundable: true, RefundableToSource: true,
			ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive}
		if err := f.repo.CreateWalletType(f.ctx, wt); err != nil {
			t.Fatalf("wallet type: %v", err)
		}
		w, _, err := f.wallets.Open(f.ctx, f.customer, wt)
		if err != nil {
			t.Fatalf("wallet: %v", err)
		}
		f.mains[code] = w
	}
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{mock.Name}, Banks: []string{"hdfc"}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	return f
}

func (f *fixture) deliver(h http.Header, b []byte) {
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

// topup captures a top-up of amount into a product's MAIN and returns it with
// its provider id.
func (f *fixture) topup(product string, amount int64) (*dao.Payment, string) {
	f.t.Helper()
	var p *dao.Payment
	var a *dao.PaymentAttempt
	err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		var err error
		p, a, err = f.payments.CreateTopup(ctx, payment.TopupRequest{ProductID: f.products[product].ID,
			Customer: f.customer, Wallet: f.mains[product], Amount: amount, Currency: "INR"})
		return err
	})
	if err != nil {
		f.t.Fatalf("top-up: %v", err)
	}
	f.mock.Pay(*a.ProviderPaymentID)
	f.deliver(f.mock.Webhook(*a.ProviderPaymentID, "payment.captured"))
	return p, *a.ProviderPaymentID
}

func (f *fixture) ingest() int {
	f.t.Helper()
	n, err := f.recon.Ingest(f.ctx, mock.Name)
	if err != nil {
		f.t.Fatalf("ingest: %v", err)
	}
	return n
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

func (f *fixture) openBreaks() map[dao.Classification]int64 {
	f.t.Helper()
	breaks, err := f.repo.ListBreaks(f.ctx, filter.AllProducts(), dao.BreakOpen, 100)
	if err != nil {
		f.t.Fatalf("breaks: %v", err)
	}
	out := map[dao.Classification]int64{}
	for _, b := range breaks {
		out[b.Classification] += b.Amount
	}
	return out
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

// Phase 8's exit criterion: a day of synthetic traffic across two products —
// top-ups, a refund, a lost chargeback — settles in one bank credit and
// reconciles to zero breaks, with each product's fees kept apart.
func TestADayOfTrafficReconcilesToZeroBreaks(t *testing.T) {
	f := setup(t, recon.Config{})
	z1, _ := f.topup("zshala", 100000)
	f.topup("zshala", 50000)
	f.topup("bappaapp", 80000)
	_, disputedID := f.topup("bappaapp", 20000)

	refund, err := f.payments.CreateRefund(f.ctx, payment.RefundRequest{PaymentID: z1.ID, Amount: 30000,
		ReasonCode: "customer_request", RequestedBy: "op"})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	f.mock.ProcessRefund(*refund.ProviderRefundID)
	f.deliver(f.mock.RefundWebhook(*refund.ProviderRefundID, "refund.processed"))

	dispute := f.mock.OpenDispute(disputedID, 20000, "fraud")
	f.deliver(f.mock.DisputeWebhook(dispute, "dispute.created"))
	f.mock.ResolveDispute(dispute, false)
	f.deliver(f.mock.DisputeWebhook(dispute, "dispute.closed"))

	f.mock.Settle(200, nil) // 2% fee plus 18% GST on it
	if n := f.ingest(); n != 1 {
		t.Fatalf("ingested %d settlements, want 1", n)
	}

	if breaks := f.openBreaks(); len(breaks) != 0 {
		t.Errorf("breaks = %v, want none", breaks)
	}
	if got := f.balance(ledger.PSPReceivable(mock.Name)); got != 0 {
		t.Errorf("receivable = %d, want 0 — everything captured was settled", got)
	}
	if got := f.balance(ledger.PSPSuspense(mock.Name)); got != 0 {
		t.Errorf("suspense = %d, want 0", got)
	}
	// Fees: 2% of each product's own payments; GST on them is reclaimable input tax.
	if got := f.balance(ledger.ProductPSPFees("zshala")); got != 3000 {
		t.Errorf("zshala fees = %d, want 3000 (2%% of 150000)", got)
	}
	if got := f.balance(ledger.ProductPSPFees("bappaapp")); got != 2000 {
		t.Errorf("bappaapp fees = %d, want 2000 (2%% of 100000)", got)
	}
	if got := f.balance(ledger.InputTaxCredit); got != 900 {
		t.Errorf("input tax credit = %d, want 900 (18%% of 5000)", got)
	}
	gross := int64(250000 - 30000 - 20000)
	if got := f.balance(ledger.Bank("hdfc")); got != gross-5000-900 {
		t.Errorf("bank = %d, want %d", got, gross-5000-900)
	}
	settled, _ := f.repo.GetPaymentByID(f.ctx, z1.ID)
	if settled.SettledAt == nil {
		t.Error("a matched payment was not marked settled")
	}

	// Ingesting again books nothing twice.
	if n := f.ingest(); n != 0 {
		t.Errorf("re-ingest took %d settlements, want 0", n)
	}
	f.assertLedgerHealthy()
}

// Each kind of mismatch is detected and classified, and its amount — and
// only its amount — lands in suspense.
func TestInjectedMismatchesAreClassified(t *testing.T) {
	f := setup(t, recon.Config{})
	_, a := f.topup("zshala", 10000)
	_, b := f.topup("zshala", 20000)
	f.topup("bappaapp", 30000)

	f.mock.Settle(0, func(st *provider.Settlement) {
		for i := range st.Items {
			switch st.Items[i].ProviderRef {
			case a: // the provider claims more than we captured
				st.Items[i].Gross, st.Items[i].Net = 10500, 10500
			case b: // its fee arithmetic does not add up
				st.Items[i].Fee = 100
			}
		}
		// A payment nobody here has heard of, and a line repeated.
		st.Items = append(st.Items,
			provider.SettlementItem{Kind: provider.SettlePayment, ProviderRef: "mockpay_unknown", Gross: 777, Net: 777},
			st.Items[len(st.Items)-1])
	})
	f.ingest()

	breaks := f.openBreaks()
	want := map[dao.Classification]int64{
		dao.AmountMismatch:  500,
		dao.FeeMismatch:     100,
		dao.MissingInLedger: 777,
		dao.Duplicate:       30000,
	}
	for class, amount := range want {
		if breaks[class] != amount {
			t.Errorf("%s break = %d, want %d (all: %v)", class, breaks[class], amount, breaks)
		}
	}
	if got := f.balance(ledger.PSPSuspense(mock.Name)); got != -(500 + 100 + 777 + 30000) {
		t.Errorf("suspense = %d, want the unexplained %d held there", got, 500+100+777+30000)
	}
	f.assertLedgerHealthy()
}

// A payment the provider never settles is flagged — once — and the break
// closes itself when the settlement turns up late.
func TestMissingAtProviderIsFlaggedAndClosedWhenLate(t *testing.T) {
	f := setup(t, recon.Config{SettleWithin: time.Nanosecond})
	_, late := f.topup("zshala", 10000)

	f.mock.Settle(0, func(st *provider.Settlement) {
		st.Items = nil // this batch leaves it out
	})
	f.ingest()
	time.Sleep(time.Millisecond)

	for range 2 {
		if _, err := f.recon.FlagUnsettled(f.ctx, mock.Name); err != nil {
			t.Fatalf("flag: %v", err)
		}
	}
	if breaks := f.openBreaks(); breaks[dao.MissingAtProvider] != 10000 {
		t.Fatalf("breaks = %v, want one missing_at_provider of 10000", breaks)
	}

	// The next settlement includes it after all.
	f.mock.Settle(0, func(st *provider.Settlement) {
		st.Items = []provider.SettlementItem{{Kind: provider.SettlePayment, ProviderRef: late, Gross: 10000, Net: 10000}}
	})
	f.ingest()
	if breaks := f.openBreaks(); len(breaks) != 0 {
		t.Errorf("breaks = %v, want the late one closed", breaks)
	}
	f.assertLedgerHealthy()
}
