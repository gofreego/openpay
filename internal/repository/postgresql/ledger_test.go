package postgresql

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func truncateLedger(t *testing.T, repo *Repository) {
	t.Helper()
	// The immutability triggers fire on DELETE but not on TRUNCATE, which is
	// exactly why tests can clean up while production cannot edit history.
	if _, err := repo.connManager.Primary().ExecContext(context.Background(),
		`TRUNCATE ledger_postings, ledger_journals, ledger_balances, ledger_accounts,
		          wallet_types, customers, audit_log, service_credentials, products
		 RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate ledger (have migrations run?): %v", err)
	}
}

func account(t *testing.T, repo *Repository, code string, accountType dao.AccountType) *dao.LedgerAccount {
	t.Helper()
	a := &dao.LedgerAccount{
		PublicID: ids.New(ids.LedgerAccount), Code: code, Type: accountType,
		Currency: "INR", OwnerKind: dao.OwnerPlatform, Status: dao.AccountActive,
	}
	if err := repo.CreateLedgerAccount(context.Background(), a); err != nil {
		t.Fatalf("create account %q: %v", code, err)
	}
	return a
}

func leg(a *dao.LedgerAccount, direction dao.Direction, amount int64) *dao.Posting {
	return &dao.Posting{AccountID: a.ID, Direction: direction, Amount: amount, Currency: "INR"}
}

func journal(externalID string, postings ...*dao.Posting) *dao.Journal {
	return &dao.Journal{
		PublicID: ids.New(ids.LedgerJournal), ExternalID: externalID,
		Kind: dao.JournalTopup, Postings: postings,
	}
}

func post(t *testing.T, repo *Repository, j *dao.Journal) error {
	t.Helper()
	return repo.WithTx(context.Background(), func(ctx context.Context) error {
		return repo.PostJournal(ctx, j)
	})
}

func natural(t *testing.T, repo *Repository, a *dao.LedgerAccount) int64 {
	t.Helper()
	b, err := repo.GetBalance(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("balance for %q: %v", a.Code, err)
	}
	return b.Natural(a.Type)
}

// The worked example from the plan: a customer tops up ₹500, which the PSP owes
// us and which we now owe the customer.
func TestPostJournalTopup(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:razorpay:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:cus_9:zshala:MAIN", dao.AccountLiability)

	j := journal("payment:pay_1:capture", leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 50000))
	if err := post(t, repo, j); err != nil {
		t.Fatalf("post: %v", err)
	}

	// The asset we hold and the liability we owe both read as +500.
	if got := natural(t, repo, psp); got != 50000 {
		t.Errorf("psp receivable = %d, want 50000", got)
	}
	if got := natural(t, repo, wallet); got != 50000 {
		t.Errorf("wallet = %d, want 50000 — crediting a liability increases what we owe", got)
	}
}

// A journal must balance, and must balance in each currency separately: one
// that nets to zero only by cancelling rupees against dollars is two mistakes,
// not a transaction.
func TestPostJournalRejectsImbalance(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	cases := []struct {
		name string
		j    *dao.Journal
	}{
		{"debits exceed credits", journal("j1", leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 40000))},
		{"single posting", journal("j2", leg(psp, dao.Debit, 50000))},
		{"zero amount", journal("j3", leg(psp, dao.Debit, 0), leg(wallet, dao.Credit, 0))},
		{"negative amount", journal("j4", leg(psp, dao.Debit, -100), leg(wallet, dao.Credit, -100))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := post(t, repo, tc.j); err == nil {
				t.Error("an unbalanced journal was accepted")
			}
		})
	}

	t.Run("balances only across currencies", func(t *testing.T) {
		usd := &dao.LedgerAccount{
			PublicID: ids.New(ids.LedgerAccount), Code: "psp:usd", Type: dao.AccountAsset,
			Currency: "USD", OwnerKind: dao.OwnerPlatform, Status: dao.AccountActive,
		}
		if err := repo.CreateLedgerAccount(context.Background(), usd); err != nil {
			t.Fatalf("create usd account: %v", err)
		}

		j := journal("j5",
			&dao.Posting{AccountID: psp.ID, Direction: dao.Debit, Amount: 100, Currency: "INR"},
			&dao.Posting{AccountID: usd.ID, Direction: dao.Credit, Amount: 100, Currency: "USD"},
		)
		err := post(t, repo, j)
		if !apperrors.Is(err, apperrors.LedgerImbalance) {
			t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.LedgerImbalance)
		}
	})
}

// Nothing is written when a journal is rejected: a half-posted transaction is
// worse than a refused one.
func TestRejectedJournalLeavesNoTrace(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	_ = post(t, repo, journal("bad", leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 40000)))

	var journals, postings int
	db := repo.connManager.Primary()
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_journals").Scan(&journals); err != nil {
		t.Fatalf("count journals: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_postings").Scan(&postings); err != nil {
		t.Fatalf("count postings: %v", err)
	}
	if journals != 0 || postings != 0 {
		t.Errorf("rejected journal left %d journals and %d postings behind", journals, postings)
	}
	if got := natural(t, repo, psp); got != 0 {
		t.Errorf("balance moved to %d despite the journal being rejected", got)
	}
}

// The external id is what makes a retried payment capture safe.
func TestPostJournalIsIdempotentOnExternalID(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	first := journal("payment:pay_1:capture", leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 50000))
	if err := post(t, repo, first); err != nil {
		t.Fatalf("first post: %v", err)
	}

	second := journal("payment:pay_1:capture", leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 50000))
	if err := post(t, repo, second); err != nil {
		t.Fatalf("replayed post should succeed: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("replay created journal %d, want the original %d", second.ID, first.ID)
	}
	if got := natural(t, repo, wallet); got != 50000 {
		t.Errorf("wallet = %d, want 50000 — the replay posted a second time", got)
	}
}

// The guarantee that matters most: concurrent identical captures credit once.
func TestConcurrentIdenticalJournalsPostOnce(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	const racers = 15
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	errs := make([]error, racers)

	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			<-barrier
			errs[i] = post(t, repo, journal("payment:pay_1:capture",
				leg(psp, dao.Debit, 50000), leg(wallet, dao.Credit, 50000)))
		}()
	}
	close(barrier)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d failed: %v", i, err)
		}
	}
	if got := natural(t, repo, wallet); got != 50000 {
		t.Errorf("wallet = %d, want 50000 — %d concurrent identical journals were not deduplicated", got, racers)
	}
}

// Concurrent *different* spends against one wallet must not overdraw it. This
// is the test that a naive read-then-write implementation fails.
func TestConcurrentSpendsCannotOverdraw(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)
	income := account(t, repo, "income:sales", dao.AccountIncome)

	// Fund the wallet with exactly 10 spends worth.
	if err := post(t, repo, journal("fund", leg(psp, dao.Debit, 1000), leg(wallet, dao.Credit, 1000))); err != nil {
		t.Fatalf("fund: %v", err)
	}

	const attempts = 30
	const spend = 100

	var wg sync.WaitGroup
	barrier := make(chan struct{})
	results := make([]error, attempts)

	wg.Add(attempts)
	for i := range attempts {
		go func() {
			defer wg.Done()
			<-barrier
			results[i] = post(t, repo, journal(fmt.Sprintf("spend:%d", i),
				leg(wallet, dao.Debit, spend), leg(income, dao.Credit, spend)))
		}()
	}
	close(barrier)
	wg.Wait()

	var succeeded int
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if !apperrors.Is(err, apperrors.InsufficientBalance) {
			t.Errorf("unexpected failure: %v", err)
		}
	}

	if succeeded != 10 {
		t.Errorf("%d spends succeeded, want exactly 10 — the wallet held %d and each spent %d",
			succeeded, 1000, spend)
	}
	if got := natural(t, repo, wallet); got != 0 {
		t.Errorf("wallet = %d, want 0", got)
	}
	if got := natural(t, repo, income); got != int64(succeeded)*spend {
		t.Errorf("income = %d, want %d", got, int64(succeeded)*spend)
	}
}

// Two journals touching the same accounts in opposite orders would deadlock if
// each locked in the order its postings happened to be listed. Locking by
// ascending account id is an order both agree on without coordinating.
func TestOppositeOrderJournalsDoNotDeadlock(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	a := account(t, repo, "wallet:a", dao.AccountLiability)
	b := account(t, repo, "wallet:b", dao.AccountLiability)
	funder := account(t, repo, "psp:receivable", dao.AccountAsset)

	if err := post(t, repo, journal("fund:a", leg(funder, dao.Debit, 100000), leg(a, dao.Credit, 100000))); err != nil {
		t.Fatalf("fund a: %v", err)
	}
	if err := post(t, repo, journal("fund:b", leg(funder, dao.Debit, 100000), leg(b, dao.Credit, 100000))); err != nil {
		t.Fatalf("fund b: %v", err)
	}

	const rounds = 40
	var wg sync.WaitGroup
	errs := make(chan error, rounds*2)

	wg.Add(rounds * 2)
	for i := range rounds {
		// a → b
		go func() {
			defer wg.Done()
			errs <- post(t, repo, journal(fmt.Sprintf("ab:%d", i),
				leg(a, dao.Debit, 10), leg(b, dao.Credit, 10)))
		}()
		// b → a, postings listed the other way round
		go func() {
			defer wg.Done()
			errs <- post(t, repo, journal(fmt.Sprintf("ba:%d", i),
				leg(b, dao.Debit, 10), leg(a, dao.Credit, 10)))
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("transfer failed, likely a deadlock: %v", err)
		}
	}

	// Every transfer was matched by one the other way, so both end where they started.
	if got := natural(t, repo, a); got != 100000 {
		t.Errorf("wallet a = %d, want 100000", got)
	}
	if got := natural(t, repo, b); got != 100000 {
		t.Errorf("wallet b = %d, want 100000", got)
	}
}

func TestOverdraftIsRefusedUnlessAllowed(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	wallet := account(t, repo, "wallet:main", dao.AccountLiability)
	income := account(t, repo, "income:sales", dao.AccountIncome)

	err := post(t, repo, journal("overdraw", leg(wallet, dao.Debit, 100), leg(income, dao.Credit, 100)))
	if !apperrors.Is(err, apperrors.InsufficientBalance) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.InsufficientBalance)
	}

	overdraftable := &dao.LedgerAccount{
		PublicID: ids.New(ids.LedgerAccount), Code: "wallet:credit", Type: dao.AccountLiability,
		Currency: "INR", OwnerKind: dao.OwnerCustomer, Status: dao.AccountActive,
		AllowNegative: true,
	}
	if err := repo.CreateLedgerAccount(ctx, overdraftable); err != nil {
		t.Fatalf("create overdraftable account: %v", err)
	}

	if err := post(t, repo, journal("overdraw-allowed",
		leg(overdraftable, dao.Debit, 100), leg(income, dao.Credit, 100))); err != nil {
		t.Errorf("an account permitting a negative balance was still refused: %v", err)
	}
	if got := natural(t, repo, overdraftable); got != -100 {
		t.Errorf("balance = %d, want -100", got)
	}
}

// A posting in a currency its account does not hold would still add up, but the
// total would be the sum of two different things.
func TestPostingCurrencyMustMatchItsAccount(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	j := journal("mismatch",
		&dao.Posting{AccountID: psp.ID, Direction: dao.Debit, Amount: 100, Currency: "USD"},
		&dao.Posting{AccountID: wallet.ID, Direction: dao.Credit, Amount: 100, Currency: "USD"},
	)
	err := post(t, repo, j)
	if !apperrors.Is(err, apperrors.CurrencyMismatch) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.CurrencyMismatch)
	}
}

// balance_after lets a statement show a running balance without re-summing
// history, so it must actually match the balance at that point.
func TestBalanceAfterTracksTheRunningBalance(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	for i, amount := range []int64{500, 300, 200} {
		if err := post(t, repo, journal(fmt.Sprintf("topup:%d", i),
			leg(psp, dao.Debit, amount), leg(wallet, dao.Credit, amount))); err != nil {
			t.Fatalf("topup %d: %v", i, err)
		}
	}

	postings, err := repo.ListAccountPostings(ctx, wallet.ID, 10, 0)
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(postings) != 3 {
		t.Fatalf("got %d postings, want 3", len(postings))
	}

	// Newest first: raw balances of -1000, -800, -500 for a credit-normal account.
	want := []int64{-1000, -800, -500}
	for i, posting := range postings {
		if posting.BalanceAfter != want[i] {
			t.Errorf("posting %d balance_after = %d, want %d", i, posting.BalanceAfter, want[i])
		}
	}
}

// An account may legitimately appear twice in one journal; its balance must end
// at the combined figure rather than at whichever leg was written last.
func TestAccountAppearingTwiceInOneJournal(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)
	recovery := account(t, repo, "income:fee_recovery", dao.AccountIncome)

	// A DEDUCTED top-up: the customer pays 1000, we charge 50 for it, and the
	// wallet nets 950. The wallet is both credited and debited in one journal.
	j := journal("topup-with-fee",
		leg(psp, dao.Debit, 1000),
		leg(wallet, dao.Credit, 1000),
		leg(wallet, dao.Debit, 50),
		leg(recovery, dao.Credit, 50),
	)
	if err := post(t, repo, j); err != nil {
		t.Fatalf("post: %v", err)
	}

	if got := natural(t, repo, wallet); got != 950 {
		t.Errorf("wallet = %d, want 950 — both legs must apply, not just the last", got)
	}
	if got := natural(t, repo, psp); got != 1000 {
		t.Errorf("psp = %d, want 1000", got)
	}
	if got := natural(t, repo, recovery); got != 50 {
		t.Errorf("fee recovery = %d, want 50", got)
	}
}

// The append-only rule is enforced by the database, so it holds even against a
// console session or a well-meant fix.
func TestLedgerRowsCannotBeEdited(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)
	j := journal("topup", leg(psp, dao.Debit, 1000), leg(wallet, dao.Credit, 1000))
	if err := post(t, repo, j); err != nil {
		t.Fatalf("post: %v", err)
	}

	db := repo.connManager.Primary()
	cases := []struct {
		name string
		sql  string
	}{
		{"update a posting amount", `UPDATE ledger_postings SET amount = 999999 WHERE journal_id = $1`},
		{"delete a posting", `DELETE FROM ledger_postings WHERE journal_id = $1`},
		{"update a journal memo", `UPDATE ledger_journals SET memo = 'edited' WHERE id = $1`},
		{"delete a journal", `DELETE FROM ledger_journals WHERE id = $1`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, tc.sql, j.ID); err == nil {
				t.Error("the ledger accepted an edit; history must be append-only")
			}
		})
	}
}

// Closing an account must stop it receiving postings, or closure means nothing.
func TestClosedAccountsRefusePostings(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	if _, err := repo.connManager.Primary().ExecContext(ctx,
		`UPDATE ledger_accounts SET status = 'closed' WHERE id = $1`, wallet.ID); err != nil {
		t.Fatalf("close account: %v", err)
	}

	err := post(t, repo, journal("after-close", leg(psp, dao.Debit, 100), leg(wallet, dao.Credit, 100)))
	if !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.FailedPrecondition)
	}
}

// Posting outside a transaction would let a journal and its postings commit
// separately, which is the one thing the ledger cannot survive.
func TestPostJournalRequiresATransaction(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)

	err := repo.PostJournal(context.Background(),
		journal("no-tx", leg(psp, dao.Debit, 100), leg(wallet, dao.Credit, 100)))
	if err == nil {
		t.Error("posting outside a transaction was accepted")
	}
}

// The global invariant: across every account, debits equal credits. If this
// ever fails, money has been created or destroyed.
func TestLedgerSumsToZeroGlobally(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()

	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet := account(t, repo, "wallet:main", dao.AccountLiability)
	income := account(t, repo, "income:sales", dao.AccountIncome)
	fees := account(t, repo, "expense:psp_fees", dao.AccountExpense)

	if err := post(t, repo, journal("topup",
		leg(psp, dao.Debit, 49000), leg(fees, dao.Debit, 1000), leg(wallet, dao.Credit, 50000))); err != nil {
		t.Fatalf("topup: %v", err)
	}
	if err := post(t, repo, journal("purchase",
		leg(wallet, dao.Debit, 30000), leg(income, dao.Credit, 30000))); err != nil {
		t.Fatalf("purchase: %v", err)
	}

	var postingSum, balanceSum int64
	db := repo.connManager.Primary()
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(direction * amount), 0) FROM ledger_postings`).Scan(&postingSum); err != nil {
		t.Fatalf("sum postings: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(raw_balance), 0) FROM ledger_balances`).Scan(&balanceSum); err != nil {
		t.Fatalf("sum balances: %v", err)
	}

	if postingSum != 0 {
		t.Errorf("postings sum to %d, want 0 — money was created or destroyed", postingSum)
	}
	if balanceSum != 0 {
		t.Errorf("balances sum to %d, want 0", balanceSum)
	}

	// And the materialised balances must agree with the postings that produced
	// them, account by account.
	rows, err := db.QueryContext(ctx, `
		SELECT b.account_id, b.raw_balance, COALESCE(SUM(p.direction * p.amount), 0)
		FROM ledger_balances b
		LEFT JOIN ledger_postings p ON p.account_id = b.account_id
		GROUP BY b.account_id, b.raw_balance`)
	if err != nil {
		t.Fatalf("compare balances: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var accountID, materialised, derived int64
		if err := rows.Scan(&accountID, &materialised, &derived); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if materialised != derived {
			t.Errorf("account %d: balance says %d, postings say %d", accountID, materialised, derived)
		}
	}
}
