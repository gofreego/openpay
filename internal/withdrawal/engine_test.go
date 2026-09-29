package withdrawal_test

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
	"github.com/gofreego/openpay/internal/withdrawal"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

const webhookPrefix = "/openpay/v1/webhooks/"

type fixture struct {
	t           *testing.T
	ctx         context.Context
	repo        *postgresql.Repository
	mock        *mock.Provider
	wallets     *wallet.Engine
	payments    *payment.Engine
	withdrawals *withdrawal.Engine
	webhooks    http.Handler

	product     *dao.Product
	customer    *dao.Customer
	cash        *dao.Wallet
	beneficiary *dao.Beneficiary
}

// setup gives a customer ₹10,000 in a withdrawable CASH wallet whose type
// requires at least ₹100 per withdrawal and approval above ₹5,000.
func setup(t *testing.T, cfg withdrawal.Config) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), repo: testsupport.Repository(t)}
	f.mock = mock.New("test-secret", "https://mock.test/checkout/")
	registry := provider.NewRegistry([]string{mock.Name}, f.mock)
	f.wallets = wallet.New(f.repo, wallet.Limits{})
	f.payments = payment.New(f.repo, registry, f.wallets, payment.Config{})
	f.withdrawals = withdrawal.New(f.repo, registry, f.wallets, f.payments, cfg)
	f.webhooks = payment.WebhookHandler(webhookPrefix, registry, f.repo)

	f.product = &dao.Product{PublicID: ids.New(ids.Product), Code: "zshala", Name: "Zshala", Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := f.repo.CreateProduct(f.ctx, f.product); err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{mock.Name}, Banks: []string{"hdfc"}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	f.customer = &dao.Customer{PublicID: ids.New(ids.Customer), ExternalRef: "user-1", Status: dao.CustomerActive}
	if _, err := f.repo.UpsertCustomer(f.ctx, f.customer); err != nil {
		t.Fatalf("customer: %v", err)
	}

	min, threshold, approvedAt, approver, ref := int64(10000), int64(500000), time.Now(), "compliance", "COMP-1"
	cashType := &dao.WalletType{PublicID: ids.New(ids.WalletType), ProductID: &f.product.ID, Scope: dao.WalletScopeProduct,
		Code: "CASH", Name: "Cash", Currency: "INR", Fundable: true, Withdrawable: true, RefundableToSource: true,
		ExpiryPolicy: dao.ExpiryNone, Status: dao.WalletTypeActive,
		MinWithdrawalAmount: &min, WithdrawalApprovalThreshold: &threshold,
		WithdrawableApprovedBy: &approver, WithdrawableApprovedAt: &approvedAt, WithdrawableApprovalRef: &ref}
	if err := f.repo.CreateWalletType(f.ctx, cashType); err != nil {
		t.Fatalf("wallet type: %v", err)
	}
	var err error
	if f.cash, _, err = f.wallets.Open(f.ctx, f.customer, cashType); err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if _, err := f.wallets.Fund(f.ctx, wallet.FundRequest{Wallet: f.cash, Amount: 1_000_000, Provider: mock.Name,
		ProductID: f.product.ID, ExternalID: "payment:seed:capture"}); err != nil {
		t.Fatalf("fund: %v", err)
	}
	account, ifsc := "12345678901", "HDFC0000001"
	if f.beneficiary, err = f.withdrawals.AddBeneficiary(f.ctx, &dao.Beneficiary{CustomerID: f.customer.ID,
		Kind: "bank_account", Name: "Asha Rao", AccountNumber: &account, IFSC: &ifsc}); err != nil {
		t.Fatalf("beneficiary: %v", err)
	}
	return f
}

func (f *fixture) request(amount int64, by string) (*dao.Withdrawal, error) {
	var x *dao.Withdrawal
	err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
		var err error
		x, err = f.withdrawals.Request(ctx, withdrawal.Request{Wallet: f.cash, Beneficiary: f.beneficiary, Amount: amount, RequestedBy: by})
		return err
	})
	return x, err
}

func (f *fixture) mustRequest(amount int64) *dao.Withdrawal {
	f.t.Helper()
	x, err := f.request(amount, "scr_zshala_backend")
	if err != nil {
		f.t.Fatalf("request: %v", err)
	}
	return x
}

func (f *fixture) webhook(x *dao.Withdrawal) {
	f.t.Helper()
	h, b := f.mock.PayoutWebhook(*x.ProviderPayoutID, "payout.updated")
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

func (f *fixture) reload(x *dao.Withdrawal) *dao.Withdrawal {
	f.t.Helper()
	got, err := f.repo.GetWithdrawalByPublicID(f.ctx, x.PublicID)
	if err != nil {
		f.t.Fatalf("reload: %v", err)
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

func (f *fixture) wallet() int64 {
	f.t.Helper()
	b, _ := f.repo.GetBalance(f.ctx, f.cash.LedgerAccountID)
	return b.Natural(dao.AccountLiability)
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

// Below the type's minimum, a withdrawal is refused and nothing moves.
func TestBelowMinimumIsRefused(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	_, err := f.request(9999, "scr_backend")
	wantCode(t, "₹99.99 against a ₹100 minimum", err, apperrors.WalletOperationDenied)
	if got := f.wallet(); got != 1_000_000 {
		t.Errorf("wallet = %d, want untouched", got)
	}
}

// Up to the threshold, a withdrawal goes straight out: wallet → in transit →
// bank, no person involved.
func TestWithdrawalWithinThresholdGoesOutAutomatically(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	x := f.mustRequest(500000) // exactly the threshold

	if x.RequiresApproval || x.Status != dao.WithdrawalProcessing {
		t.Fatalf("withdrawal %s (approval %t), want processing without approval", x.Status, x.RequiresApproval)
	}
	if f.wallet() != 500000 || f.balance(ledger.PayoutsInTransit) != 500000 {
		t.Errorf("wallet %d, in transit %d; want 500000 each", f.wallet(), f.balance(ledger.PayoutsInTransit))
	}

	f.mock.CompletePayout(*x.ProviderPayoutID)
	f.webhook(x)
	if got := f.reload(x); got.Status != dao.WithdrawalPaid || got.PaidAt == nil {
		t.Errorf("withdrawal is %s, want paid", got.Status)
	}
	if f.balance(ledger.PayoutsInTransit) != 0 || f.balance(ledger.Bank("hdfc")) != -500000 {
		t.Errorf("in transit %d, bank %d; want 0 and -500000", f.balance(ledger.PayoutsInTransit), f.balance(ledger.Bank("hdfc")))
	}
	f.assertLedgerHealthy()
}

// Above the threshold, the money is reserved at once but nothing is sent
// until someone other than the requester approves it.
func TestWithdrawalAboveThresholdWaitsForApproval(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	x, err := f.request(600000, "op_asha")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !x.RequiresApproval || x.Status != dao.WithdrawalPendingApproval || x.ProviderPayoutID != nil {
		t.Fatalf("withdrawal %s (payout %v), want pending approval with nothing sent", x.Status, x.ProviderPayoutID)
	}
	if got := f.wallet(); got != 400000 {
		t.Errorf("wallet = %d, want 400000 — the amount is reserved while it waits", got)
	}

	_, err = f.withdrawals.Approve(f.ctx, x.ID, "op_asha", "looks fine")
	wantCode(t, "approving your own withdrawal", err, apperrors.PermissionDenied)
	if n := f.mock.PayoutCount(); n != 0 {
		t.Fatalf("%d payouts reached the provider before approval", n)
	}
	_, err = f.withdrawals.Approve(f.ctx, x.ID, "op_ravi", "")
	wantCode(t, "approving without a reason", err, apperrors.InvalidArgument)

	approved, err := f.withdrawals.Approve(f.ctx, x.ID, "op_ravi", "customer verified by phone")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != dao.WithdrawalProcessing || approved.DecidedBy == nil || *approved.DecidedBy != "op_ravi" {
		t.Errorf("after approval %s by %v, want processing by op_ravi", approved.Status, approved.DecidedBy)
	}
	_, err = f.withdrawals.Approve(f.ctx, x.ID, "op_meera", "again")
	wantCode(t, "approving twice", err, apperrors.FailedPrecondition)

	f.mock.CompletePayout(*approved.ProviderPayoutID)
	f.webhook(approved)
	if got := f.reload(x).Status; got != dao.WithdrawalPaid {
		t.Errorf("withdrawal is %s, want paid", got)
	}
	f.assertLedgerHealthy()
}

func TestRejectedWithdrawalReturnsTheMoney(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	x := f.mustRequest(700000)
	rejected, err := f.withdrawals.Reject(f.ctx, x.ID, "op_ravi", "destination looks wrong")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Status != dao.WithdrawalRejected || f.wallet() != 1_000_000 || f.balance(ledger.PayoutsInTransit) != 0 {
		t.Errorf("status %s, wallet %d, in transit %d; want rejected, all back, nothing in transit",
			rejected.Status, f.wallet(), f.balance(ledger.PayoutsInTransit))
	}
	f.assertLedgerHealthy()
}

// Phase 9's exit criterion: a withdrawal debits the wallet, lands in transit,
// confirms to the bank — and a forced failure afterwards returns the money to
// the wallet with a clean trail.
func TestPaidThenBouncedReturnsTheMoney(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	x := f.mustRequest(300000)
	f.mock.CompletePayout(*x.ProviderPayoutID)
	f.webhook(x)
	if got := f.reload(x).Status; got != dao.WithdrawalPaid {
		t.Fatalf("withdrawal is %s, want paid", got)
	}

	f.mock.ReversePayout(*x.ProviderPayoutID, "beneficiary account closed")
	f.webhook(x)

	got := f.reload(x)
	if got.Status != dao.WithdrawalReversed || got.FailureReason == nil || *got.FailureReason != "beneficiary account closed" {
		t.Errorf("withdrawal %s (%v), want reversed with the bank's reason", got.Status, got.FailureReason)
	}
	if f.wallet() != 1_000_000 || f.balance(ledger.Bank("hdfc")) != 0 || f.balance(ledger.PayoutsInTransit) != 0 {
		t.Errorf("wallet %d, bank %d, in transit %d; want everything back where it started",
			f.wallet(), f.balance(ledger.Bank("hdfc")), f.balance(ledger.PayoutsInTransit))
	}
	restore, err := f.repo.GetJournalByExternalID(f.ctx, "withdrawal:"+x.PublicID+":reversed")
	if err != nil || restore.ReversesJournalID == nil {
		t.Errorf("reversal journal %v (%v), want one reversing the debit", restore, err)
	}
	f.assertLedgerHealthy()
}

func TestFailedPayoutReturnsTheMoney(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	x := f.mustRequest(300000)
	f.mock.FailPayout(*x.ProviderPayoutID, "IFSC invalid")
	f.webhook(x)
	if got := f.reload(x); got.Status != dao.WithdrawalFailed || f.wallet() != 1_000_000 {
		t.Errorf("withdrawal %s, wallet %d; want failed with the money back", got.Status, f.wallet())
	}
	f.assertLedgerHealthy()
}

// A provider timeout leaves the withdrawal approved with its money reserved;
// the poller submits it again under the same id.
func TestPayoutTimeoutIsRetried(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	f.mock.FailNextPayout(provider.ErrUnavailable("mock: timed out"))
	x := f.mustRequest(300000)
	if x.Status != dao.WithdrawalApproved || f.wallet() != 700000 {
		t.Fatalf("withdrawal %s, wallet %d; want approved with 300000 reserved", x.Status, f.wallet())
	}
	polled, err := f.withdrawals.Poll(f.ctx, x)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if polled.Status != dao.WithdrawalProcessing || polled.ProviderPayoutID == nil {
		t.Errorf("after poll %s, want processing", polled.Status)
	}
}

func TestWithdrawalRefusals(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	_, err := f.request(2_000_000, "scr")
	wantCode(t, "more than the wallet holds", err, apperrors.InsufficientBalance)

	bad, zeros := "00001111", "HDFC0000001"
	failed, err := f.withdrawals.AddBeneficiary(f.ctx, &dao.Beneficiary{CustomerID: f.customer.ID, Kind: "bank_account",
		Name: "Nobody", AccountNumber: &bad, IFSC: &zeros})
	if err != nil {
		t.Fatalf("add failing beneficiary: %v", err)
	}
	_, err = f.withdrawals.Request(f.ctx, withdrawal.Request{Wallet: f.cash, Beneficiary: failed, Amount: 20000, RequestedBy: "scr"})
	wantCode(t, "to an unverified destination", err, apperrors.FailedPrecondition)

	cooling := setup(t, withdrawal.Config{BeneficiaryCooling: time.Hour})
	_, err = cooling.request(20000, "scr")
	wantCode(t, "to a destination still cooling", err, apperrors.FailedPrecondition)
}

// The wallet type's daily limit holds under concurrency: ten ₹1,000
// withdrawals at once against a ₹3,000 daily limit give exactly three.
func TestDailyWithdrawalLimitHoldsUnderConcurrency(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	testsupport.Exec(t, `UPDATE wallet_types SET daily_withdrawal_limit = 300000 WHERE code = 'CASH'`)

	const attempts = 10
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	errs := make([]error, attempts)
	wg.Add(attempts)
	for i := range attempts {
		go func() {
			defer wg.Done()
			<-barrier
			_, errs[i] = f.request(100000, fmt.Sprintf("scr_%d", i))
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
	if ok != 3 || f.wallet() != 700000 {
		t.Errorf("%d withdrawals succeeded, wallet %d; want exactly 3 and 700000", ok, f.wallet())
	}
	f.assertLedgerHealthy()
}

// A failed withdrawal gave the money back, so it no longer counts towards
// today's limit.
func TestFailedWithdrawalFreesTheDailyLimit(t *testing.T) {
	f := setup(t, withdrawal.Config{})
	testsupport.Exec(t, `UPDATE wallet_types SET daily_withdrawal_limit = 300000 WHERE code = 'CASH'`)
	x := f.mustRequest(300000)
	if _, err := f.request(10000, "scr"); !apperrors.Is(err, apperrors.WalletOperationDenied) {
		t.Fatalf("over the daily limit: error code = %q", apperrors.CodeOf(err))
	}
	f.mock.FailPayout(*x.ProviderPayoutID, "bank down")
	f.webhook(x)
	if _, err := f.request(10000, "scr"); err != nil {
		t.Errorf("after the failed payout freed the limit: %v", err)
	}
}

// Per-person caps count every withdrawal the customer makes today.
func TestCustomerDailyWithdrawalCount(t *testing.T) {
	f := setup(t, withdrawal.Config{MaxCustomerDailyWithdrawals: 2})
	f.mustRequest(10000)
	f.mustRequest(10000)
	_, err := f.request(10000, "scr")
	wantCode(t, "third withdrawal of the day", err, apperrors.WalletOperationDenied)
}
