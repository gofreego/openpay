package postgresql

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

func newHold(externalID string, a *dao.LedgerAccount, amount int64) *dao.Hold {
	return &dao.Hold{
		PublicID: ids.New(ids.LedgerHold), ExternalID: externalID,
		AccountID: a.ID, Amount: amount, Currency: "INR",
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func placeHold(t *testing.T, repo *Repository, h *dao.Hold) error {
	t.Helper()
	return repo.WithTx(context.Background(), func(ctx context.Context) error {
		return repo.PlaceHold(ctx, h)
	})
}

func captureHold(t *testing.T, repo *Repository, holdExternalID string, j *dao.Journal) (*dao.Hold, error) {
	t.Helper()
	var hold *dao.Hold
	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		var err error
		hold, _, err = repo.CaptureHold(ctx, holdExternalID, j)
		return err
	})
	return hold, err
}

func releaseHold(t *testing.T, repo *Repository, holdExternalID string) (*dao.Hold, error) {
	t.Helper()
	var hold *dao.Hold
	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		var err error
		hold, err = repo.ReleaseHold(ctx, holdExternalID)
		return err
	})
	return hold, err
}

func balanceOf(t *testing.T, repo *Repository, a *dao.LedgerAccount) *dao.Balance {
	t.Helper()
	b, err := repo.GetBalance(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("balance for %q: %v", a.Code, err)
	}
	return b
}

// fundedWallet returns a wallet holding amount, and an income account to spend into.
func fundedWallet(t *testing.T, repo *Repository, amount int64) (wallet, income *dao.LedgerAccount) {
	t.Helper()
	psp := account(t, repo, "psp:receivable", dao.AccountAsset)
	wallet = account(t, repo, "wallet:main", dao.AccountLiability)
	income = account(t, repo, "income:sales", dao.AccountIncome)
	if err := post(t, repo, journal("fund", leg(psp, dao.Debit, amount), leg(wallet, dao.Credit, amount))); err != nil {
		t.Fatalf("fund: %v", err)
	}
	return wallet, income
}

// Held money is still the customer's, but it is no longer spendable: the
// balance stays, the available figure drops, and a spend that would reach into
// the reservation is refused.
func TestHoldReducesAvailableNotBalance(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("order:1:hold", wallet, 700)); err != nil {
		t.Fatalf("place hold: %v", err)
	}

	b := balanceOf(t, repo, wallet)
	if got := b.Natural(wallet.Type); got != 1000 {
		t.Errorf("balance = %d, want 1000 — a hold moves no money", got)
	}
	if got := b.Available(wallet.Type); got != 300 {
		t.Errorf("available = %d, want 300", got)
	}

	err := post(t, repo, journal("spend", leg(wallet, dao.Debit, 400), leg(income, dao.Credit, 400)))
	if !apperrors.Is(err, apperrors.InsufficientBalance) {
		t.Errorf("spending into held funds: error code = %q, want %q", apperrors.CodeOf(err), apperrors.InsufficientBalance)
	}
	if err := post(t, repo, journal("spend-ok", leg(wallet, dao.Debit, 300), leg(income, dao.Credit, 300))); err != nil {
		t.Errorf("spending exactly what is available was refused: %v", err)
	}
}

func TestHoldRefusedBeyondAvailable(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, _ := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("h1", wallet, 600)); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	err := placeHold(t, repo, newHold("h2", wallet, 500))
	if !apperrors.Is(err, apperrors.InsufficientBalance) {
		t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.InsufficientBalance)
	}
	// The refused hold left nothing behind.
	if _, err := repo.GetHoldByExternalID(context.Background(), "h2"); !apperrors.Is(err, apperrors.NotFound) {
		t.Errorf("a refused hold was recorded: %v", err)
	}
	if got := balanceOf(t, repo, wallet).Held; got != 600 {
		t.Errorf("held = %d, want 600", got)
	}
}

func TestPlaceHoldIsIdempotent(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, _ := fundedWallet(t, repo, 1000)

	first := newHold("order:1:hold", wallet, 400)
	if err := placeHold(t, repo, first); err != nil {
		t.Fatalf("place: %v", err)
	}
	again := newHold("order:1:hold", wallet, 400)
	if err := placeHold(t, repo, again); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.PublicID != first.PublicID {
		t.Errorf("replay returned hold %q, want the original %q", again.PublicID, first.PublicID)
	}
	if got := balanceOf(t, repo, wallet).Held; got != 400 {
		t.Errorf("held = %d, want 400 — the same hold reserved twice", got)
	}

	err := placeHold(t, repo, newHold("order:1:hold", wallet, 500))
	if !apperrors.Is(err, apperrors.AlreadyExists) {
		t.Errorf("same external id, different amount: error code = %q, want %q",
			apperrors.CodeOf(err), apperrors.AlreadyExists)
	}
}

// The case that needs the release and the posting in one locked pass: a wallet
// holding exactly the captured amount, all of it reserved by the hold being
// captured.
func TestCaptureHoldUsesTheReservation(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("order:1:hold", wallet, 1000)); err != nil {
		t.Fatalf("place: %v", err)
	}

	hold, err := captureHold(t, repo, "order:1:hold",
		journal("order:1:capture", leg(wallet, dao.Debit, 1000), leg(income, dao.Credit, 1000)))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if hold.Status != dao.HoldCaptured || hold.CapturedAmount == nil || *hold.CapturedAmount != 1000 {
		t.Errorf("hold = %+v, want captured for 1000", hold)
	}

	b := balanceOf(t, repo, wallet)
	if b.Natural(wallet.Type) != 0 || b.Held != 0 {
		t.Errorf("wallet balance %d held %d, want 0 and 0", b.Natural(wallet.Type), b.Held)
	}
	if got := natural(t, repo, income); got != 1000 {
		t.Errorf("income = %d, want 1000", got)
	}
}

// Capturing less than was held frees the rest.
func TestPartialCaptureReleasesTheRemainder(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("h", wallet, 300)); err != nil {
		t.Fatalf("place: %v", err)
	}
	if _, err := captureHold(t, repo, "h",
		journal("capture", leg(wallet, dao.Debit, 250), leg(income, dao.Credit, 250))); err != nil {
		t.Fatalf("capture: %v", err)
	}

	b := balanceOf(t, repo, wallet)
	if got := b.Natural(wallet.Type); got != 750 {
		t.Errorf("balance = %d, want 750", got)
	}
	if got := b.Available(wallet.Type); got != 750 {
		t.Errorf("available = %d, want 750 — the uncaptured 50 is still reserved", got)
	}
}

func TestCaptureMustTakeFromTheHeldAccountWithinTheHold(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)
	other := account(t, repo, "wallet:other", dao.AccountLiability)

	if err := placeHold(t, repo, newHold("h", wallet, 300)); err != nil {
		t.Fatalf("place: %v", err)
	}

	cases := map[string]*dao.Journal{
		"more than held":   journal("c1", leg(wallet, dao.Debit, 301), leg(income, dao.Credit, 301)),
		"another account":  journal("c2", leg(other, dao.Debit, 100), leg(income, dao.Credit, 100)),
		"credits the hold": journal("c3", leg(income, dao.Debit, 100), leg(wallet, dao.Credit, 100)),
	}
	for name, j := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := captureHold(t, repo, "h", j)
			if !apperrors.Is(err, apperrors.InvalidArgument) {
				t.Errorf("error code = %q, want %q", apperrors.CodeOf(err), apperrors.InvalidArgument)
			}
		})
	}

	if got := balanceOf(t, repo, wallet).Held; got != 300 {
		t.Errorf("held = %d, want 300 — refused captures must leave the hold in place", got)
	}
}

func TestCaptureHoldIsIdempotent(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("h", wallet, 300)); err != nil {
		t.Fatalf("place: %v", err)
	}
	capture := func(externalID string) error {
		_, err := captureHold(t, repo, "h", journal(externalID, leg(wallet, dao.Debit, 300), leg(income, dao.Credit, 300)))
		return err
	}

	if err := capture("capture"); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := capture("capture"); err != nil {
		t.Errorf("retrying the same capture failed: %v", err)
	}
	if got := natural(t, repo, wallet); got != 700 {
		t.Errorf("wallet = %d, want 700 — a retried capture spent twice", got)
	}

	if err := capture("capture-2"); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("capturing again with a new journal: error code = %q, want %q",
			apperrors.CodeOf(err), apperrors.FailedPrecondition)
	}
}

func TestReleasedAndCapturedHoldsAreFinal(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, income := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("released", wallet, 200)); err != nil {
		t.Fatalf("place: %v", err)
	}
	if err := placeHold(t, repo, newHold("captured", wallet, 200)); err != nil {
		t.Fatalf("place: %v", err)
	}

	if _, err := releaseHold(t, repo, "released"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := releaseHold(t, repo, "released"); err != nil {
		t.Errorf("releasing twice should be a no-op: %v", err)
	}
	if _, err := captureHold(t, repo, "released",
		journal("late-capture", leg(wallet, dao.Debit, 200), leg(income, dao.Credit, 200))); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("capturing a released hold: error code = %q, want %q", apperrors.CodeOf(err), apperrors.FailedPrecondition)
	}

	if _, err := captureHold(t, repo, "captured",
		journal("capture", leg(wallet, dao.Debit, 200), leg(income, dao.Credit, 200))); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := releaseHold(t, repo, "captured"); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("releasing a captured hold: error code = %q, want %q", apperrors.CodeOf(err), apperrors.FailedPrecondition)
	}

	b := balanceOf(t, repo, wallet)
	if b.Natural(wallet.Type) != 800 || b.Held != 0 {
		t.Errorf("wallet balance %d held %d, want 800 and 0", b.Natural(wallet.Type), b.Held)
	}

	// The database refuses to reopen a resolved hold, not just this code.
	_, err := repo.connManager.Primary().ExecContext(context.Background(),
		`UPDATE ledger_holds SET status = 'active', resolved_at = NULL WHERE external_id = 'released'`)
	if err == nil {
		t.Error("a resolved hold was reopened by a direct UPDATE")
	}
}

func TestExpiryOnlyAfterExpiresAt(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	ctx := context.Background()
	wallet, _ := fundedWallet(t, repo, 1000)

	if err := placeHold(t, repo, newHold("fresh", wallet, 100)); err != nil {
		t.Fatalf("place: %v", err)
	}
	stale := newHold("stale", wallet, 200)
	stale.ExpiresAt = time.Now().Add(50 * time.Millisecond)
	if err := placeHold(t, repo, stale); err != nil {
		t.Fatalf("place: %v", err)
	}

	expire := func(externalID string) error {
		return repo.WithTx(ctx, func(ctx context.Context) error {
			_, err := repo.ExpireHold(ctx, externalID)
			return err
		})
	}
	if err := expire("stale"); !apperrors.Is(err, apperrors.FailedPrecondition) {
		t.Errorf("expiring early: error code = %q, want %q", apperrors.CodeOf(err), apperrors.FailedPrecondition)
	}

	time.Sleep(100 * time.Millisecond)

	due, err := repo.ListExpiredHolds(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(due) != 1 || due[0] != "stale" {
		t.Fatalf("expired holds = %v, want [stale]", due)
	}
	if err := expire("stale"); err != nil {
		t.Fatalf("expire: %v", err)
	}

	if got := balanceOf(t, repo, wallet).Held; got != 100 {
		t.Errorf("held = %d, want 100 — only the fresh hold remains", got)
	}
	if due, _ := repo.ListExpiredHolds(ctx, time.Now(), 10); len(due) != 0 {
		t.Errorf("expired holds after sweep = %v, want none", due)
	}
}

// Concurrent holds cannot together reserve more than the account has.
func TestConcurrentHoldsCannotOverReserve(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, _ := fundedWallet(t, repo, 1000)

	const attempts = 30
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	results := make([]error, attempts)

	wg.Add(attempts)
	for i := range attempts {
		go func() {
			defer wg.Done()
			<-barrier
			results[i] = placeHold(t, repo, newHold(fmt.Sprintf("h:%d", i), wallet, 100))
		}()
	}
	close(barrier)
	wg.Wait()

	var succeeded int
	for _, err := range results {
		if err == nil {
			succeeded++
		} else if !apperrors.Is(err, apperrors.InsufficientBalance) {
			t.Errorf("unexpected failure: %v", err)
		}
	}
	if succeeded != 10 {
		t.Errorf("%d holds succeeded, want exactly 10", succeeded)
	}
	if got := balanceOf(t, repo, wallet).Held; got != 1000 {
		t.Errorf("held = %d, want 1000", got)
	}
}

// Credits must never be refused for landing on an account whose available
// balance is short — refusing incoming money fixes nothing.
func TestCreditIsNeverRefusedForHolds(t *testing.T) {
	repo := testRepository(t)
	truncateLedger(t, repo)
	wallet, _ := fundedWallet(t, repo, 1000)
	bank := account(t, repo, "bank:current", dao.AccountAsset)

	if err := placeHold(t, repo, newHold("h", wallet, 1000)); err != nil {
		t.Fatalf("place: %v", err)
	}
	if err := post(t, repo, journal("topup", leg(bank, dao.Debit, 100), leg(wallet, dao.Credit, 100))); err != nil {
		t.Fatalf("credit to a fully held wallet was refused: %v", err)
	}
	if got := balanceOf(t, repo, wallet).Available(wallet.Type); got != 100 {
		t.Errorf("available = %d, want 100", got)
	}
}
