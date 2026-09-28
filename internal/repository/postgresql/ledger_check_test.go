package postgresql

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func invariantsFound(findings []dao.LedgerCheckFinding) map[string]int {
	found := map[string]int{}
	for _, f := range findings {
		found[f.Invariant]++
	}
	return found
}

func check(t *testing.T, repo *Repository) []dao.LedgerCheckFinding {
	t.Helper()
	findings, _, err := repo.CheckLedgerInvariants(context.Background(), 50)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return findings
}

func exec(t *testing.T, repo *Repository, query string, args ...any) {
	t.Helper()
	if _, err := repo.connManager.Primary().ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// A ledger built only through the engine — postings, holds, captures,
// releases — satisfies every invariant.
func TestHealthyLedgerHasNoFindings(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := post(t, repo, journal("spend", leg(wallet, dao.Debit, 100), leg(income, dao.Credit, 100))); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if err := placeHold(t, repo, newHold("captured", wallet, 200)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := captureHold(t, repo, "captured",
		journal("capture", leg(wallet, dao.Debit, 150), leg(income, dao.Credit, 150))); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := placeHold(t, repo, newHold("open", wallet, 300)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := placeHold(t, repo, newHold("released", wallet, 50)); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := releaseHold(t, repo, "released"); err != nil {
		t.Fatalf("release: %v", err)
	}

	if findings := check(t, repo); len(findings) != 0 {
		t.Errorf("healthy ledger reported %v", findings)
	}
}

// Each kind of corruption the checker exists for, made the way it would
// really happen: by writing around the engine.
func TestCheckerFindsCorruption(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(t *testing.T, repo *Repository, wallet, income *dao.LedgerAccount)
		want    []string
	}{
		{
			name: "materialised balance edited",
			corrupt: func(t *testing.T, repo *Repository, wallet, _ *dao.LedgerAccount) {
				exec(t, repo, `UPDATE ledger_balances SET raw_balance = raw_balance - 500 WHERE account_id = $1`, wallet.ID)
			},
			want: []string{"balance_drift"},
		},
		{
			name: "posting inserted without its other leg",
			corrupt: func(t *testing.T, repo *Repository, wallet, _ *dao.LedgerAccount) {
				exec(t, repo, `INSERT INTO ledger_postings (journal_id, account_id, direction, amount, currency, seq, balance_after)
					SELECT id, $1, -1, 70, 'INR', 99, -1070 FROM ledger_journals WHERE external_id = 'fund'`, wallet.ID)
			},
			want: []string{"journal_unbalanced", "ledger_unbalanced", "balance_drift"},
		},
		{
			name: "held edited",
			corrupt: func(t *testing.T, repo *Repository, wallet, _ *dao.LedgerAccount) {
				exec(t, repo, `UPDATE ledger_balances SET held = 400 WHERE account_id = $1`, wallet.ID)
			},
			want: []string{"held_drift"},
		},
		{
			name: "wallet pushed negative",
			corrupt: func(t *testing.T, repo *Repository, wallet, _ *dao.LedgerAccount) {
				// Keeps balance and postings agreeing, so only the overdraft shows.
				exec(t, repo, `UPDATE ledger_balances SET held = 5000 WHERE account_id = $1`, wallet.ID)
			},
			want: []string{"held_drift", "negative_balance"},
		},
		{
			name: "journal without postings",
			corrupt: func(t *testing.T, repo *Repository, _, _ *dao.LedgerAccount) {
				exec(t, repo, `INSERT INTO ledger_journals (public_id, external_id, kind) VALUES ('jrn_x', 'orphan', 'adjustment')`)
			},
			want: []string{"journal_too_few_postings"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepository(t)
			truncateLedger(t, repo)
			wallet, income := fundedWallet(t, repo, 1000)

			tc.corrupt(t, repo, wallet, income)

			found := invariantsFound(check(t, repo))
			for _, want := range tc.want {
				if found[want] == 0 {
					t.Errorf("%s not reported; found %v", want, found)
				}
			}
			if len(found) != len(tc.want) {
				t.Errorf("found %v, want exactly %v", found, tc.want)
			}
		})
	}
}

func TestCheckerTruncatesFindings(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	for i := range 5 {
		a := account(t, repo, fmt.Sprintf("asset:%d", i), dao.AccountAsset)
		exec(t, repo, `UPDATE ledger_balances SET raw_balance = 1 WHERE account_id = $1`, a.ID)
	}

	findings, truncated, err := repo.CheckLedgerInvariants(context.Background(), 3)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(findings) != 3 || !truncated {
		t.Errorf("%d findings, truncated=%t; want 3 and true", len(findings), truncated)
	}
}

// A checker that reports drift on a healthy ledger under load is one people
// learn to ignore. Each invariant compares both sides within one statement —
// balances against postings, balance_after against the running sum — so
// postings committing mid-check cannot split them. This keeps it that way if
// an invariant is ever rewritten as two queries.
func TestCheckerHasNoFalsePositivesUnderConcurrentPosting(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1_000_000)

	var (
		stop    atomic.Bool
		wg      sync.WaitGroup
		posted  atomic.Int64
		postErr atomic.Value
	)
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				err := post(t, repo, journal(fmt.Sprintf("w%d:%d", w, i),
					leg(wallet, dao.Debit, 1), leg(income, dao.Credit, 1)))
				if err != nil {
					postErr.Store(err)
					return
				}
				posted.Add(1)
			}
		}()
	}

	for range 15 {
		if findings := check(t, repo); len(findings) != 0 {
			t.Errorf("drift reported during concurrent posting: %v", findings)
			break
		}
	}
	stop.Store(true)
	wg.Wait()

	if err := postErr.Load(); err != nil {
		t.Fatalf("posting failed: %v", err)
	}
	if posted.Load() == 0 {
		t.Fatal("nothing was posted concurrently, so the test proved nothing")
	}
}

func TestRunCheckRecordsTheRun(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()
	wallet, _ := fundedWallet(t, repo, 1000)

	operator := "op_1"
	ok, err := ledger.RunCheck(ctx, repo, dao.LedgerCheckManual, &operator)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ok.Status != dao.LedgerCheckOK {
		t.Errorf("status = %q, want ok", ok.Status)
	}

	exec(t, repo, `UPDATE ledger_balances SET raw_balance = raw_balance + 1 WHERE account_id = $1`, wallet.ID)
	drift, err := ledger.RunCheck(ctx, repo, dao.LedgerCheckScheduled, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if drift.Status != dao.LedgerCheckDrift {
		t.Errorf("status = %q, want drift", drift.Status)
	}

	runs, err := repo.ListLedgerCheckRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("%d runs recorded, want 2", len(runs))
	}
	latest := runs[0]
	if latest.PublicID != drift.PublicID || len(latest.Findings) != 1 ||
		latest.Findings[0].Invariant != "balance_drift" || latest.Findings[0].Subject != wallet.Code {
		t.Errorf("latest run = %+v, want the drift run with one balance_drift on %s", latest, wallet.Code)
	}
	if runs[1].TriggeredBy == nil || *runs[1].TriggeredBy != operator {
		t.Errorf("manual run triggered_by = %v, want %q", runs[1].TriggeredBy, operator)
	}
}

// Property: any sequence of operations the engine accepts leaves every
// invariant intact. Random journals across random accounts, holds placed,
// captured and released, many of them refused for insufficient funds — and
// the checker must find nothing, whatever mix was accepted.
func TestRandomWorkloadPreservesInvariants(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(uint64(seed), 0))

	types := []dao.AccountType{dao.AccountAsset, dao.AccountLiability, dao.AccountIncome, dao.AccountExpense, dao.AccountEquity}
	var accounts []*dao.LedgerAccount
	for i := range 8 {
		a := &dao.LedgerAccount{
			PublicID: ids.New(ids.LedgerAccount), Code: fmt.Sprintf("fuzz:%d", i),
			Type: types[i%len(types)], Currency: "INR", OwnerKind: dao.OwnerPlatform,
			Status: dao.AccountActive, AllowNegative: i%2 == 0,
		}
		if err := repo.CreateLedgerAccount(context.Background(), a); err != nil {
			t.Fatalf("create account: %v", err)
		}
		accounts = append(accounts, a)
	}
	pick := func() *dao.LedgerAccount { return accounts[rng.IntN(len(accounts))] }

	var accepted, refused int
	var activeHolds []*dao.Hold
	for i := range 300 {
		var err error
		switch op := rng.IntN(10); {
		case op < 6:
			// A balanced journal of 2–4 legs: random debits, one credit for the sum.
			legs := 1 + rng.IntN(3)
			var postings []*dao.Posting
			var total int64
			for range legs {
				amount := int64(1 + rng.IntN(500))
				total += amount
				postings = append(postings, leg(pick(), dao.Debit, amount))
			}
			postings = append(postings, leg(pick(), dao.Credit, total))
			err = post(t, repo, journal(fmt.Sprintf("fuzz:%d", i), postings...))
		case op < 8:
			h := newHold(fmt.Sprintf("fuzz:hold:%d", i), pick(), int64(1+rng.IntN(300)))
			if err = placeHold(t, repo, h); err == nil {
				activeHolds = append(activeHolds, h)
			}
		case len(activeHolds) > 0:
			h := activeHolds[0]
			activeHolds = activeHolds[1:]
			if op == 8 {
				_, err = releaseHold(t, repo, h.ExternalID)
			} else {
				account, _ := repo.getLedgerAccountByID(context.Background(), h.AccountID)
				take := int64(1 + rng.IntN(int(h.Amount)))
				// Take from the held account in whichever direction reduces it.
				out, in := dao.Debit, dao.Credit
				if account.Type.NormalSign() == 1 {
					out, in = dao.Credit, dao.Debit
				}
				// The other side must be a different account, or the capture takes nothing.
				other := pick()
				for other.ID == h.AccountID {
					other = pick()
				}
				_, err = captureHold(t, repo, h.ExternalID, journal(fmt.Sprintf("fuzz:capture:%d", i),
					&dao.Posting{AccountID: h.AccountID, Direction: out, Amount: take, Currency: "INR"},
					&dao.Posting{AccountID: other.ID, Direction: in, Amount: take, Currency: "INR"}))
			}
		}

		switch {
		case err == nil:
			accepted++
		case apperrors.Is(err, apperrors.InsufficientBalance):
			refused++
		default:
			t.Fatalf("operation %d failed unexpectedly: %v", i, err)
		}
	}

	t.Logf("%d operations accepted, %d refused for insufficient funds", accepted, refused)
	if findings := check(t, repo); len(findings) != 0 {
		t.Errorf("random workload (seed %d) broke invariants: %v", seed, findings)
	}
}

// A reversal nets its original to zero on every account it touched.
func TestReversalNetsToZero(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)
	tax := account(t, repo, "liability:gst_payable", dao.AccountLiability)

	original := journal("purchase", leg(wallet, dao.Debit, 300), leg(income, dao.Credit, 254), leg(tax, dao.Credit, 46))
	if err := post(t, repo, original); err != nil {
		t.Fatalf("purchase: %v", err)
	}

	// The reversal is the original with every direction flipped.
	var legs []*dao.Posting
	for _, p := range original.Postings {
		legs = append(legs, &dao.Posting{AccountID: p.AccountID, Direction: -p.Direction, Amount: p.Amount, Currency: p.Currency})
	}
	reversal := journal("purchase:reversal", legs...)
	reversal.Kind = dao.JournalReversal
	reversal.ReversesJournalID = &original.ID
	if err := post(t, repo, reversal); err != nil {
		t.Fatalf("reversal: %v", err)
	}

	if got := natural(t, repo, wallet); got != 1000 {
		t.Errorf("wallet = %d, want 1000", got)
	}
	for _, a := range []*dao.LedgerAccount{income, tax} {
		if got := natural(t, repo, a); got != 0 {
			t.Errorf("%s = %d, want 0", a.Code, got)
		}
	}
	if findings := check(t, repo); len(findings) != 0 {
		t.Errorf("findings after reversal: %v", findings)
	}
}
