package wallet_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/repository/postgresql"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

type fixture struct {
	t       *testing.T
	ctx     context.Context
	repo    *postgresql.Repository
	engine  *wallet.Engine
	product *dao.Product
	main    *dao.WalletType
	bonus   *dao.WalletType
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: testsupport.Repository(t)}
	f.engine = wallet.New(f.repo)

	f.product = f.newProduct("zshala")
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{"mock"}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	f.main = f.walletType(f.product, "MAIN", func(wt *dao.WalletType) { wt.Fundable = true })
	f.bonus = f.walletType(f.product, "BONUS", func(wt *dao.WalletType) { wt.Grantable = true })
	return f
}

func (f *fixture) newProduct(code string) *dao.Product {
	f.t.Helper()
	p := &dao.Product{PublicID: ids.New(ids.Product), Code: code, Name: code,
		Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := f.repo.CreateProduct(f.ctx, p); err != nil {
		f.t.Fatalf("create product: %v", err)
	}
	if _, err := ledger.EnsureAccounts(f.ctx, f.repo, ledger.ProductChart(p)); err != nil {
		f.t.Fatalf("product chart: %v", err)
	}
	return p
}

func (f *fixture) walletType(product *dao.Product, code string, configure func(*dao.WalletType)) *dao.WalletType {
	f.t.Helper()
	wt := &dao.WalletType{
		PublicID: ids.New(ids.WalletType), ProductID: &product.ID, Scope: dao.WalletScopeProduct,
		Code: code, Name: code, Currency: "INR", ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive,
	}
	configure(wt)
	if err := f.repo.CreateWalletType(f.ctx, wt); err != nil {
		f.t.Fatalf("create wallet type %s: %v", code, err)
	}
	return wt
}

func (f *fixture) customer(ref string) *dao.Customer {
	f.t.Helper()
	c := &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: ref, Status: dao.CustomerActive}
	if _, err := f.repo.UpsertCustomer(f.ctx, c); err != nil {
		f.t.Fatalf("customer: %v", err)
	}
	return c
}

func (f *fixture) open(c *dao.Customer, wt *dao.WalletType) *dao.Wallet {
	f.t.Helper()
	w, err := f.engine.Open(f.ctx, c, wt)
	if err != nil {
		f.t.Fatalf("open %s wallet: %v", wt.Code, err)
	}
	return w
}

func (f *fixture) balance(w *dao.Wallet) int64 {
	f.t.Helper()
	b, err := f.repo.GetBalance(f.ctx, w.LedgerAccountID)
	if err != nil {
		f.t.Fatalf("balance: %v", err)
	}
	return b.Natural(dao.AccountLiability)
}

func (f *fixture) fund(w *dao.Wallet, amount int64, ref string) error {
	_, err := f.engine.Fund(f.ctx, wallet.FundRequest{Wallet: w, Amount: amount, Provider: "mock",
		ProductID: f.product.ID, ExternalID: "payment:" + ref + ":capture"})
	return err
}

func (f *fixture) grant(w *dao.Wallet, amount int64, ref string) error {
	_, err := f.engine.Grant(f.ctx, wallet.GrantRequest{Wallet: w, Amount: amount,
		FundingProductID: f.product.ID, ReasonCode: "promotion", Reference: ref})
	return err
}

func (f *fixture) spend(w *dao.Wallet, amount int64, ref string) error {
	_, err := f.engine.Spend(f.ctx, wallet.SpendRequest{Wallet: w, Amount: amount,
		CounterAccountCode: ledger.ProductSales(f.product.Code), ProductID: f.product.ID,
		Kind: dao.JournalPurchase, ExternalID: "order:" + ref})
	return err
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

// Phase 3's exit scenario: top up MAIN, grant BONUS, spend from each. Every
// movement lands where D10 says it must, and the ledger stays balanced.
func TestTopUpGrantAndSpend(t *testing.T) {
	f := setup(t)
	c := f.customer("user-1")
	main, bonus := f.open(c, f.main), f.open(c, f.bonus)

	if err := f.fund(main, 50000, "pay_1"); err != nil {
		t.Fatalf("fund MAIN: %v", err)
	}
	if err := f.grant(bonus, 10000, "welcome"); err != nil {
		t.Fatalf("grant BONUS: %v", err)
	}
	if err := f.spend(main, 30000, "ord_1:main"); err != nil {
		t.Fatalf("spend MAIN: %v", err)
	}
	if err := f.spend(bonus, 4000, "ord_1:bonus"); err != nil {
		t.Fatalf("spend BONUS: %v", err)
	}

	if got := f.balance(main); got != 20000 {
		t.Errorf("MAIN = %d, want 20000", got)
	}
	if got := f.balance(bonus); got != 6000 {
		t.Errorf("BONUS = %d, want 6000", got)
	}

	// Granted money is funded by the promotions expense, not by a receivable:
	// that is what keeps it out of "real customer money".
	promotions, _ := f.repo.GetLedgerAccountByCode(f.ctx, ledger.ProductPromotions("zshala"))
	if b, _ := f.repo.GetBalance(f.ctx, promotions.ID); b.Natural(promotions.Type) != 10000 {
		t.Errorf("promotions expense = %d, want 10000", b.Natural(promotions.Type))
	}
	receivable, _ := f.repo.GetLedgerAccountByCode(f.ctx, ledger.PSPReceivable("mock"))
	if b, _ := f.repo.GetBalance(f.ctx, receivable.ID); b.Natural(receivable.Type) != 50000 {
		t.Errorf("PSP receivable = %d, want 50000", b.Natural(receivable.Type))
	}

	// The capability guard, not an accident of missing code, keeps them apart.
	wantCode(t, "fund BONUS", f.fund(bonus, 100, "pay_2"), apperrors.WalletOperationDenied)
	wantCode(t, "grant MAIN", f.grant(main, 100, "oops"), apperrors.WalletOperationDenied)

	f.assertLedgerHealthy()
}

// Wallets are created on first reference, and a burst of first references
// makes one wallet and one ledger account.
func TestOpenIsIdempotentUnderConcurrency(t *testing.T) {
	f := setup(t)
	c := f.customer("user-1")

	const n = 15
	var wg sync.WaitGroup
	wallets := make([]*dao.Wallet, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			wallets[i], errs[i] = f.engine.Open(f.ctx, c, f.main)
		}()
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("open %d: %v", i, errs[i])
		}
		if wallets[i].ID != wallets[0].ID || wallets[i].LedgerAccountID != wallets[0].LedgerAccountID {
			t.Errorf("open %d returned wallet %d/account %d, want %d/%d", i,
				wallets[i].ID, wallets[i].LedgerAccountID, wallets[0].ID, wallets[0].LedgerAccountID)
		}
	}
	account, err := f.repo.GetLedgerAccountByCode(f.ctx, ledger.WalletAccount(c.PublicID, "zshala", "MAIN"))
	if err != nil {
		t.Fatalf("wallet account: %v", err)
	}
	if account.Type != dao.AccountLiability || account.ProductID == nil || *account.ProductID != f.product.ID {
		t.Errorf("wallet account = %+v, want a zshala liability — we owe the customer", account)
	}
}

func TestGrantIsIdempotentAndHonest(t *testing.T) {
	f := setup(t)
	bonus := f.open(f.customer("user-1"), f.bonus)

	if err := f.grant(bonus, 500, "campaign-7"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := f.grant(bonus, 500, "campaign-7"); err != nil {
		t.Errorf("retried grant failed: %v", err)
	}
	if got := f.balance(bonus); got != 500 {
		t.Errorf("BONUS = %d, want 500 — a retried grant credited twice", got)
	}
	// Reusing a reference for a different amount is a bug in the caller, and
	// silently returning the first grant would hide it.
	wantCode(t, "same reference, different amount", f.grant(bonus, 900, "campaign-7"), apperrors.AlreadyExists)

	_, err := f.engine.Grant(f.ctx, wallet.GrantRequest{Wallet: bonus, Amount: 100,
		FundingProductID: f.product.ID, ReasonCode: "because", Reference: "x"})
	wantCode(t, "unknown reason code", err, apperrors.InvalidArgument)
}

// A product funds grants to its own wallets only: otherwise BappaApp could
// give away value that Zshala's P&L pays for.
func TestGrantMustBeFundedByTheWalletsProduct(t *testing.T) {
	f := setup(t)
	bonus := f.open(f.customer("user-1"), f.bonus)
	other := f.newProduct("bappaapp")

	_, err := f.engine.Grant(f.ctx, wallet.GrantRequest{Wallet: bonus, Amount: 100,
		FundingProductID: other.ID, ReasonCode: "promotion", Reference: "x"})
	wantCode(t, "grant funded by another product", err, apperrors.WalletOperationDenied)
}

// The maximum balance holds under concurrency, because it is checked with
// the wallet's account locked rather than before posting.
func TestMaxBalanceHoldsUnderConcurrentCredits(t *testing.T) {
	f := setup(t)
	limit := int64(1000)
	capped := f.walletType(f.product, "CAPPED", func(wt *dao.WalletType) {
		wt.Grantable = true
		wt.MaxBalance = &limit
	})
	w := f.open(f.customer("user-1"), capped)

	const attempts = 20
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	errs := make([]error, attempts)
	wg.Add(attempts)
	for i := range attempts {
		go func() {
			defer wg.Done()
			<-barrier
			errs[i] = f.grant(w, 100, fmt.Sprintf("g%d", i))
		}()
	}
	close(barrier)
	wg.Wait()

	var ok int
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !apperrors.Is(err, apperrors.WalletOperationDenied) {
			t.Errorf("unexpected failure: %v", err)
		}
	}
	if ok != 10 || f.balance(w) != 1000 {
		t.Errorf("%d grants succeeded, balance %d; want exactly 10 and 1000", ok, f.balance(w))
	}
	f.assertLedgerHealthy()
}

func TestDailyLoadLimit(t *testing.T) {
	f := setup(t)
	limit := int64(500)
	loadCapped := f.walletType(f.product, "DAILY", func(wt *dao.WalletType) {
		wt.Fundable = true
		wt.DailyLoadLimit = &limit
	})
	w := f.open(f.customer("user-1"), loadCapped)

	if err := f.fund(w, 300, "p1"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// Spending does not free load allowance: the limit is on what arrives.
	if err := f.spend(w, 300, "o1"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	wantCode(t, "load over the daily limit", f.fund(w, 300, "p2"), apperrors.WalletOperationDenied)
	if err := f.fund(w, 200, "p3"); err != nil {
		t.Errorf("load up to exactly the limit refused: %v", err)
	}
}

func TestTransfer(t *testing.T) {
	f := setup(t)
	gift := f.walletType(f.product, "GIFT", func(wt *dao.WalletType) {
		wt.Fundable = true
		wt.Transferable = true
	})
	alice, bob := f.customer("alice"), f.customer("bob")
	aliceGift, bobGift := f.open(alice, gift), f.open(bob, gift)

	if err := f.fund(aliceGift, 1000, "p1"); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if _, err := f.engine.Transfer(f.ctx, wallet.TransferRequest{From: aliceGift, To: bobGift, Amount: 400, Reference: "t1"}); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if f.balance(aliceGift) != 600 || f.balance(bobGift) != 400 {
		t.Errorf("alice %d, bob %d; want 600 and 400", f.balance(aliceGift), f.balance(bobGift))
	}

	_, err := f.engine.Transfer(f.ctx, wallet.TransferRequest{From: aliceGift, To: bobGift, Amount: 5000, Reference: "t2"})
	wantCode(t, "transfer more than held", err, apperrors.InsufficientBalance)

	// MAIN is not transferable, and wallets of different types never trade:
	// that would turn purchased money into granted money.
	aliceMain, bobMain := f.open(alice, f.main), f.open(bob, f.main)
	_, err = f.engine.Transfer(f.ctx, wallet.TransferRequest{From: aliceMain, To: bobMain, Amount: 1, Reference: "t3"})
	wantCode(t, "transfer from non-transferable MAIN", err, apperrors.WalletOperationDenied)
	_, err = f.engine.Transfer(f.ctx, wallet.TransferRequest{From: aliceGift, To: bobMain, Amount: 1, Reference: "t4"})
	wantCode(t, "transfer across types", err, apperrors.WalletOperationDenied)

	f.assertLedgerHealthy()
}

// A frozen wallet refuses the customer's own operations but still accepts an
// operator's adjustment, which is how it gets put right.
func TestFrozenWalletAcceptsOnlyAdjustments(t *testing.T) {
	f := setup(t)
	main := f.open(f.customer("user-1"), f.main)
	if err := f.fund(main, 1000, "p1"); err != nil {
		t.Fatalf("fund: %v", err)
	}
	testsupport.Exec(t, `UPDATE wallets SET status = 'frozen' WHERE id = $1`, main.ID)
	main.Status = dao.WalletFrozen

	wantCode(t, "spend from frozen wallet", f.spend(main, 100, "o1"), apperrors.WalletOperationDenied)

	journal, err := f.engine.Adjust(f.ctx, wallet.AdjustRequest{Wallet: main, Amount: 250, Direction: dao.Debit,
		ProductID: f.product.ID, ReasonCode: "error_correction", Memo: "duplicate top-up", Reference: "case-42"})
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if journal.ReasonCode == nil || *journal.ReasonCode != "error_correction" {
		t.Errorf("reason code = %v, want error_correction", journal.ReasonCode)
	}
	if got := f.balance(main); got != 750 {
		t.Errorf("MAIN = %d, want 750", got)
	}

	// An adjustment corrects a balance; it cannot create a debt the type forbids.
	_, err = f.engine.Adjust(f.ctx, wallet.AdjustRequest{Wallet: main, Amount: 5000, Direction: dao.Debit,
		ProductID: f.product.ID, ReasonCode: "error_correction", Reference: "case-43"})
	wantCode(t, "adjust below zero", err, apperrors.InsufficientBalance)
	f.assertLedgerHealthy()
}

func TestHoldAndCapture(t *testing.T) {
	f := setup(t)
	main := f.open(f.customer("user-1"), f.main)
	if err := f.fund(main, 1000, "p1"); err != nil {
		t.Fatalf("fund: %v", err)
	}

	if _, err := f.engine.Hold(f.ctx, wallet.HoldRequest{Wallet: main, Amount: 600, Reference: "ord_1",
		ExpiresAt: time.Now().Add(15 * time.Minute)}); err != nil {
		t.Fatalf("hold: %v", err)
	}
	wantCode(t, "spend into held funds", f.spend(main, 500, "o2"), apperrors.InsufficientBalance)

	if _, err := f.engine.CaptureHold(f.ctx, wallet.SpendRequest{Wallet: main, Amount: 550,
		CounterAccountCode: ledger.ProductSales("zshala"), ProductID: f.product.ID,
		Kind: dao.JournalPurchase, ExternalID: "order:ord_1:capture"}, "ord_1"); err != nil {
		t.Fatalf("capture: %v", err)
	}
	b, _ := f.repo.GetBalance(f.ctx, main.LedgerAccountID)
	if b.Natural(dao.AccountLiability) != 450 || b.Held != 0 {
		t.Errorf("balance %d held %d, want 450 and 0", b.Natural(dao.AccountLiability), b.Held)
	}
	f.assertLedgerHealthy()
}

// An archived type keeps its existing wallets working but opens no new ones.
func TestArchivedTypeOpensNoNewWallets(t *testing.T) {
	f := setup(t)
	existing := f.customer("existing")
	w := f.open(existing, f.main)

	testsupport.Exec(t, `UPDATE wallet_types SET status = 'archived' WHERE id = $1`, f.main.ID)
	f.main.Status = dao.WalletTypeArchived

	if again, err := f.engine.Open(f.ctx, existing, f.main); err != nil || again.ID != w.ID {
		t.Errorf("reopening an existing wallet of an archived type: %v", err)
	}
	_, err := f.engine.Open(f.ctx, f.customer("newcomer"), f.main)
	wantCode(t, "new wallet of archived type", err, apperrors.WalletOperationDenied)
}
