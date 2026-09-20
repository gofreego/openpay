package postgresql

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/ids"
)

// truncateIdempotency clears the table so tests do not see each other's rows.
// The schema itself comes from the migrations, which must have been applied.
func truncateIdempotency(t *testing.T, repo *Repository) {
	t.Helper()
	if _, err := repo.connManager.Primary().
		ExecContext(context.Background(), "TRUNCATE idempotency_keys"); err != nil {
		t.Fatalf("truncate idempotency_keys (have migrations run?): %v", err)
	}
}

func newKey(scope, key, fingerprint string) *dao.IdempotencyKey {
	return &dao.IdempotencyKey{
		Scope:              scope,
		Key:                key,
		RequestFingerprint: fingerprint,
		ExpiresAt:          time.Now().Add(24 * time.Hour),
	}
}

func TestClaimIdempotencyKey(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	claimed, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp1"))
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !ok {
		t.Fatal("first claim should succeed")
	}
	if claimed.Status != dao.IdempotencyInProgress {
		t.Errorf("status = %q, want in_progress", claimed.Status)
	}

	// A second claim of the same key must lose and see the existing record.
	existing, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp1"))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if ok {
		t.Fatal("second claim of the same key must not succeed")
	}
	if existing.ID != claimed.ID {
		t.Errorf("existing record id = %d, want %d", existing.ID, claimed.ID)
	}
	if existing.RequestFingerprint != "fp1" {
		t.Errorf("fingerprint = %q, want fp1", existing.RequestFingerprint)
	}
}

// scope is what lets two callers independently use the key "order-42".
func TestClaimIdempotencyKeyIsScoped(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("product_a", "order-42", "fp")); err != nil || !ok {
		t.Fatalf("claim in product_a: ok=%v err=%v", ok, err)
	}
	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("product_b", "order-42", "fp")); err != nil || !ok {
		t.Fatalf("the same key in another scope must be claimable: ok=%v err=%v", ok, err)
	}
}

// The guarantee that matters: concurrent identical requests, exactly one winner.
func TestClaimIdempotencyKeyUnderConcurrency(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)

	const racers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		losses  int
		failed  []error
		barrier = make(chan struct{})
	)

	wg.Add(racers)
	for range racers {
		go func() {
			defer wg.Done()
			<-barrier // release all at once, to actually contend
			_, ok, err := repo.ClaimIdempotencyKey(context.Background(), newKey("global", "same-key", "fp"))

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				failed = append(failed, err)
			case ok:
				wins++
			default:
				losses++
			}
		}()
	}
	close(barrier)
	wg.Wait()

	for _, err := range failed {
		t.Errorf("concurrent claim errored: %v", err)
	}
	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1 — duplicate requests would both execute", wins)
	}
	if losses != racers-1-len(failed) {
		t.Errorf("losses = %d, want %d", losses, racers-1-len(failed))
	}
}

func TestCompleteAndReplay(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp1")); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	response := []byte(`{"payment_id":"pay_1"}`)
	if err := repo.CompleteIdempotencyKey(ctx, "global", "k1", response); err != nil {
		t.Fatalf("complete: %v", err)
	}

	existing, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp1"))
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if ok {
		t.Fatal("a completed key must not be re-claimable")
	}
	if existing.Status != dao.IdempotencyCompleted {
		t.Errorf("status = %q, want completed", existing.Status)
	}
	if string(existing.ResponseBody) != string(response) {
		t.Errorf("response = %q, want %q", existing.ResponseBody, response)
	}
}

// Completing a key nobody claimed means the response could never be replayed.
// That must fail loudly rather than pass silently.
func TestCompleteWithoutClaimFails(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)

	err := repo.CompleteIdempotencyKey(context.Background(), "global", "never-claimed", []byte("{}"))
	if err == nil {
		t.Fatal("completing an unclaimed key should fail")
	}
	if !apperrors.Is(err, apperrors.Internal) {
		t.Errorf("error code = %q, want internal", apperrors.CodeOf(err))
	}
}

func TestCompleteTwiceFails(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.CompleteIdempotencyKey(ctx, "global", "k1", []byte("{}")); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if err := repo.CompleteIdempotencyKey(ctx, "global", "k1", []byte("{}")); err == nil {
		t.Error("completing an already-completed key should fail")
	}
}

func TestReleaseAllowsRetry(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.ReleaseIdempotencyKey(ctx, "global", "k1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp")); err != nil || !ok {
		t.Errorf("after release the key must be claimable again: ok=%v err=%v", ok, err)
	}
}

// Releasing a completed key would throw away a replayable response.
func TestReleaseDoesNotDropCompletedKeys(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.CompleteIdempotencyKey(ctx, "global", "k1", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := repo.ReleaseIdempotencyKey(ctx, "global", "k1"); err != nil {
		t.Fatalf("release: %v", err)
	}

	existing, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp"))
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if ok {
		t.Fatal("release must not have removed the completed key")
	}
	if string(existing.ResponseBody) != `{"ok":true}` {
		t.Errorf("stored response lost: %q", existing.ResponseBody)
	}
}

// The work commits with the response recorded, or neither happens.
func TestCompleteRollsBackWithTheWork(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp")); err != nil {
		t.Fatalf("claim: %v", err)
	}

	boom := errors.New("work failed after recording the response")
	err := repo.WithTx(ctx, func(ctx context.Context) error {
		if err := repo.CompleteIdempotencyKey(ctx, "global", "k1", []byte(`{"ok":true}`)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want %v", err, boom)
	}

	existing, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "k1", "fp"))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if existing.Status != dao.IdempotencyInProgress {
		t.Errorf("status = %q, want in_progress — a response was recorded for work that never committed", existing.Status)
	}
}

func TestDeleteExpiredIdempotencyKeys(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	expired := newKey("global", "old", "fp")
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if _, _, err := repo.ClaimIdempotencyKey(ctx, expired); err != nil {
		t.Fatalf("claim expired: %v", err)
	}
	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "fresh", "fp")); err != nil {
		t.Fatalf("claim fresh: %v", err)
	}

	deleted, err := repo.DeleteExpiredIdempotencyKeys(ctx, 100)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// The expired key is free again; the fresh one is untouched.
	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "old", "fp")); err != nil || !ok {
		t.Errorf("expired key should be claimable again: ok=%v err=%v", ok, err)
	}
	if _, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "fresh", "fp")); err != nil || ok {
		t.Errorf("fresh key should still be held: ok=%v err=%v", ok, err)
	}
}

// A key reused with a different request body is a caller bug. The stored
// fingerprint is what lets the caller be told, rather than served a response
// that belongs to a different request.
func TestFingerprintIsPreservedForConflictDetection(t *testing.T) {
	repo := testRepository(t)
	truncateIdempotency(t, repo)
	ctx := context.Background()

	if _, _, err := repo.ClaimIdempotencyKey(ctx, newKey("global", ids.New(ids.Idempotency), "fingerprint-A")); err != nil {
		t.Fatalf("claim: %v", err)
	}

	key := newKey("global", "shared", "fingerprint-A")
	if _, _, err := repo.ClaimIdempotencyKey(ctx, key); err != nil {
		t.Fatalf("claim: %v", err)
	}

	existing, ok, err := repo.ClaimIdempotencyKey(ctx, newKey("global", "shared", "fingerprint-B"))
	if err != nil {
		t.Fatalf("claim with different fingerprint: %v", err)
	}
	if ok {
		t.Fatal("key was already held; claim should not succeed")
	}
	if existing.RequestFingerprint != "fingerprint-A" {
		t.Errorf("stored fingerprint = %q, want the original", existing.RequestFingerprint)
	}
}
