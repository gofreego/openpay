package postgresql

import (
	"context"
	"sync"
	"testing"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

var testChart = ledger.ChartConfig{Providers: []string{"razorpay", "cashfree"}, Banks: []string{"hdfc"}}

func countAccounts(t *testing.T, repo *Repository) int {
	t.Helper()
	return count(t, repo, "ledger_accounts")
}

func TestEnsureChartIsIdempotent(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	zshala := &dao.Product{PublicID: ids.New(ids.Product), Code: "zshala", Name: "Zshala",
		Status: dao.ProductActive, DefaultCurrency: "INR"}
	if err := repo.CreateProduct(ctx, zshala); err != nil {
		t.Fatalf("create product: %v", err)
	}

	want := len(ledger.PlatformChart(testChart)) + len(ledger.ProductChart(zshala))

	created, err := ledger.EnsureChart(ctx, repo, testChart)
	if err != nil {
		t.Fatalf("ensure chart: %v", err)
	}
	if created != want {
		t.Errorf("created %d accounts, want %d", created, want)
	}

	again, err := ledger.EnsureChart(ctx, repo, testChart)
	if err != nil {
		t.Fatalf("ensure chart again: %v", err)
	}
	if again != 0 {
		t.Errorf("second run created %d accounts, want 0", again)
	}
	if got := countAccounts(t, repo); got != want {
		t.Errorf("ledger holds %d accounts, want %d", got, want)
	}

	// Spot-check scope: revenue is the product's, the receivable is the platform's.
	sales, err := repo.GetLedgerAccountByCode(ctx, ledger.ProductSales("zshala"))
	if err != nil {
		t.Fatalf("product sales account: %v", err)
	}
	if sales.ProductID == nil || *sales.ProductID != zshala.ID || sales.Type != dao.AccountIncome {
		t.Errorf("product sales = %+v, want an income account for product %d", sales, zshala.ID)
	}
	receivable, err := repo.GetLedgerAccountByCode(ctx, ledger.PSPReceivable("razorpay"))
	if err != nil {
		t.Fatalf("receivable: %v", err)
	}
	if receivable.ProductID != nil || receivable.Type != dao.AccountAsset {
		t.Errorf("receivable = %+v, want a platform asset account", receivable)
	}
}

// Adding a provider to the configuration adds its accounts and touches nothing else.
func TestEnsureChartPicksUpNewProviders(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	if _, err := ledger.EnsureChart(ctx, repo, testChart); err != nil {
		t.Fatalf("ensure chart: %v", err)
	}
	more := testChart
	more.Providers = append([]string{}, testChart.Providers...)
	more.Providers = append(more.Providers, "stripe")

	created, err := ledger.EnsureChart(ctx, repo, more)
	if err != nil {
		t.Fatalf("ensure chart with a new provider: %v", err)
	}
	if created != 2 {
		t.Errorf("created %d accounts, want 2 (receivable and suspense)", created)
	}
}

// An account that exists under a chart code but with a different type is a
// finding, not something to paper over: every report built on the chart's
// assumption about it would be wrong.
func TestEnsureChartRefusesAContradictingAccount(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	account(t, repo, ledger.GSTPayable, dao.AccountAsset)

	_, err := ledger.EnsureChart(context.Background(), repo, testChart)
	if !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("error code = %q, want %q (%v)", apperrors.CodeOf(err), apperrors.FailedPrecondition, err)
	}
}

// Concurrent first uses of the same account inside transactions must all
// succeed with the same row. A unique violation would abort the loser's
// transaction, so the conflict must be resolved without raising one.
func TestGetOrCreateLedgerAccountInsideConcurrentTransactions(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	const n = 20
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	accounts := make([]*dao.LedgerAccount, n)
	errs := make([]error, n)

	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			<-barrier
			errs[i] = repo.WithTx(context.Background(), func(ctx context.Context) error {
				a := &dao.LedgerAccount{
					PublicID: ids.New(ids.LedgerAccount), Code: "wallet:cus_1:zshala:MAIN",
					Type: dao.AccountLiability, Currency: "INR",
					OwnerKind: dao.OwnerCustomer, Status: dao.AccountActive,
				}
				if _, err := repo.GetOrCreateLedgerAccount(ctx, a); err != nil {
					return err
				}
				accounts[i] = a
				// Prove the transaction is still usable after losing the race.
				_, err := repo.GetBalance(ctx, a.ID)
				return err
			})
		}()
	}
	close(barrier)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d: %v", i, err)
		}
		if accounts[i].ID != accounts[0].ID {
			t.Errorf("transaction %d got account %d, want %d", i, accounts[i].ID, accounts[0].ID)
		}
	}
	if got := countAccounts(t, repo); got != 1 {
		t.Errorf("%d accounts exist, want 1", got)
	}
	if got := count(t, repo, "ledger_balances"); got != 1 {
		t.Errorf("%d balance rows exist, want 1", got)
	}
}

// Product codes are embedded in account codes, so two products must never
// produce the same one.
func TestProductChartCodesAreDistinctPerProduct(t *testing.T) {
	seen := map[string]string{}
	for _, code := range []string{"zshala", "bappaapp"} {
		p := &dao.Product{Code: code}
		for _, a := range ledger.ProductChart(p) {
			if other, dup := seen[a.Code]; dup {
				t.Errorf("%q is produced by both %s and %s", a.Code, other, code)
			}
			seen[a.Code] = code
		}
	}
	if want := 2 * len(ledger.ProductChart(&dao.Product{Code: "x"})); len(seen) != want {
		t.Errorf("expected %d distinct codes, got %d", want, len(seen))
	}
}
