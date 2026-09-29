package wallet_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/wallet"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// TestPostingLoad is a sanity-level load test of the posting engine (plan.md
// Phase 10): not a scaling exercise, just proof that it holds up at a
// multiple of expected volume and stays correct while doing it.
//
// It drives a realistic mix — top-ups, grants, spends and transfers — from
// many goroutines over a shared set of customers, so wallets and the hot
// product accounts (sales, promotions, the PSP receivable) are contended the
// way real traffic contends them. Afterwards the invariant checker must find
// nothing, and the money must add up exactly.
//
// Skipped unless OPENPAY_LOAD_TEST=1. Tunables, all optional:
//
//	OPENPAY_LOAD_WORKERS   concurrent callers           (default 16)
//	OPENPAY_LOAD_SECONDS   how long to run              (default 10)
//	OPENPAY_LOAD_CUSTOMERS customers sharing the load   (default 200)
//	OPENPAY_LOAD_MIN_TPS   fail below this rate         (default 50)
func TestPostingLoad(t *testing.T) {
	if os.Getenv("OPENPAY_LOAD_TEST") != "1" {
		t.Skip("set OPENPAY_LOAD_TEST=1 (with OPENPAY_TEST_POSTGRES=1) to run the load test")
	}
	workers := envInt("OPENPAY_LOAD_WORKERS", 16)
	duration := time.Duration(envInt("OPENPAY_LOAD_SECONDS", 10)) * time.Second
	customers := envInt("OPENPAY_LOAD_CUSTOMERS", 200)
	minTPS := float64(envInt("OPENPAY_LOAD_MIN_TPS", 50))

	f := setup(t)
	gift := f.walletType(f.product, "GIFT", func(wt *dao.WalletType) {
		wt.Fundable = true
		wt.Transferable = true
	})
	type account struct{ main, bonus, gift *dao.Wallet }
	accounts := make([]account, customers)
	for i := range accounts {
		c := f.customer(fmt.Sprintf("load-%d", i))
		accounts[i] = account{main: f.open(c, f.main), bonus: f.open(c, f.bonus), gift: f.open(c, gift)}
	}

	const (
		opFund = iota
		opGrant
		opSpend
		opTransfer
		opCount
	)
	names := [opCount]string{"fund", "grant", "spend", "transfer"}
	var (
		latencies [opCount][]time.Duration
		mu        sync.Mutex
		// What the test did, to check the ledger against afterwards.
		funded, granted, spent atomic.Int64
		refused, failed        atomic.Int64
		seq                    atomic.Int64
	)

	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 42))
			local := [opCount][]time.Duration{}
			for time.Now().Before(deadline) {
				n := seq.Add(1)
				ref := "load-" + strconv.FormatInt(n, 10)
				a := accounts[rng.IntN(len(accounts))]
				amount := int64(100 + rng.IntN(900))

				// Mostly money coming in and going out, as on a checkout;
				// transfers and grants are the minority.
				var (
					op  int
					err error
				)
				start := time.Now()
				switch r := rng.IntN(100); {
				case r < 35:
					op = opFund
					err = f.fund(a.main, amount, ref)
					if err == nil {
						funded.Add(amount)
					}
				case r < 50:
					op = opGrant
					err = f.grant(a.bonus, amount, ref)
					if err == nil {
						granted.Add(amount)
					}
				case r < 85:
					op = opSpend
					from := a.main
					if rng.IntN(3) == 0 {
						from = a.bonus
					}
					err = f.spend(from, amount/2, ref)
					if err == nil {
						spent.Add(amount / 2)
					}
				default:
					op = opTransfer
					// Anyone but themselves.
					to := accounts[(slices.Index(accounts, a)+1+rng.IntN(len(accounts)-1))%len(accounts)]
					if err = f.fund(a.gift, amount, ref+"-gift"); err == nil {
						funded.Add(amount)
						_, err = f.engine.Transfer(f.ctx, wallet.TransferRequest{
							From: a.gift, To: to.gift, Amount: amount, Reference: ref})
					}
				}
				local[op] = append(local[op], time.Since(start))

				switch {
				case err == nil:
				case apperrors.Is(err, apperrors.InsufficientBalance):
					// A spend from an empty wallet: a correct answer, not a
					// failure of the engine.
					refused.Add(1)
				default:
					failed.Add(1)
					t.Errorf("%s %s: %v", names[op], ref, err)
				}
			}
			mu.Lock()
			for op := range local {
				latencies[op] = append(latencies[op], local[op]...)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	total := 0
	for op := range latencies {
		total += len(latencies[op])
	}
	tps := float64(total) / duration.Seconds()
	t.Logf("%d operations in %s from %d workers over %d customers: %.0f ops/s (%d refused for balance, %d failed)",
		total, duration, workers, customers, tps, refused.Load(), failed.Load())
	for op, l := range latencies {
		if len(l) == 0 {
			continue
		}
		slices.Sort(l)
		pct := func(p float64) time.Duration { return l[int(float64(len(l)-1)*p)].Round(100 * time.Microsecond) }
		t.Logf("  %-8s n=%-6d p50=%-8s p95=%-8s p99=%-8s max=%s", names[op], len(l), pct(0.50), pct(0.95), pct(0.99), l[len(l)-1].Round(time.Millisecond))
	}

	if failed.Load() > 0 {
		t.Fatalf("%d operations failed under load", failed.Load())
	}
	if tps < minTPS {
		t.Errorf("%.0f ops/s is under the %.0f floor", tps, minTPS)
	}

	// Correct, not just fast: every invariant holds, and what the wallets
	// hold is exactly what went in less what came out.
	f.assertLedgerHealthy()
	var held int64
	for _, a := range accounts {
		held += f.balance(a.main) + f.balance(a.bonus) + f.balance(a.gift)
	}
	if want := funded.Load() + granted.Load() - spent.Load(); held != want {
		t.Errorf("wallets hold %d, want funded %d + granted %d - spent %d = %d",
			held, funded.Load(), granted.Load(), spent.Load(), want)
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
