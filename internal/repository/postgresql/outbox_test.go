package postgresql

import (
	"context"
	"errors"
	"testing"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/ids"
)

func truncateOutbox(t *testing.T, repo *Repository) {
	t.Helper()
	if _, err := repo.connManager.Primary().
		ExecContext(context.Background(), "TRUNCATE outbox_events"); err != nil {
		t.Fatalf("truncate outbox_events (have migrations run?): %v", err)
	}
}

func newEvent(topic, aggregateID string) *dao.OutboxEvent {
	return &dao.OutboxEvent{
		EventID:       ids.New(ids.OutboxEvent),
		Topic:         topic,
		AggregateType: "wallet",
		AggregateID:   aggregateID,
		Payload:       []byte(`{"amount":"500"}`),
	}
}

func saveEvents(t *testing.T, repo *Repository, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		if err := repo.SaveOutboxEvent(ctx, newEvent("wallet.credited", ids.New(ids.Wallet))); err != nil {
			t.Fatalf("save event %d: %v", i, err)
		}
	}
}

func TestSaveOutboxEvent(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)

	event := newEvent("wallet.credited", "wlt_1")
	if err := repo.SaveOutboxEvent(context.Background(), event); err != nil {
		t.Fatalf("save: %v", err)
	}
	if event.ID == 0 {
		t.Error("save should populate the generated id")
	}
	if event.CreatedAt.IsZero() {
		t.Error("save should populate created_at")
	}
}

// An event recorded in a transaction that rolls back must not survive: that is
// the whole point of the outbox over "write the row, then call Kafka".
func TestSaveOutboxEventRollsBackWithTheWork(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	ctx := context.Background()

	boom := errors.New("work failed")
	err := repo.WithTx(ctx, func(ctx context.Context) error {
		if err := repo.SaveOutboxEvent(ctx, newEvent("wallet.credited", "wlt_1")); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx error = %v, want %v", err, boom)
	}

	var count int
	if err := repo.connManager.Primary().
		QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("outbox rows = %d, want 0 — an event outlived the work that caused it", count)
	}
}

func TestClaimRequiresTransaction(t *testing.T) {
	repo := testRepository(t)

	// Outside a transaction the row locks would be released immediately, so
	// two drainers could claim the same events.
	if _, err := repo.ClaimUnpublishedOutboxEvents(context.Background(), 10); err == nil {
		t.Error("claiming outside a transaction should fail")
	}
}

func TestClaimReturnsOldestFirstAndRespectsLimit(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	saveEvents(t, repo, 5)

	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 3)
		if err != nil {
			return err
		}
		if len(events) != 3 {
			t.Errorf("claimed %d events, want 3", len(events))
		}
		for i := 1; i < len(events); i++ {
			if events[i-1].ID >= events[i].ID {
				t.Error("events must come back oldest first")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// SKIP LOCKED is what lets several drainers run at once. Without it the second
// transaction would block, or worse, claim the same rows.
func TestConcurrentClaimsDoNotOverlap(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	saveEvents(t, repo, 6)

	first := make(chan []int64, 1)
	second := make(chan []int64, 1)
	firstClaimed := make(chan struct{})

	go func() {
		_ = repo.WithTx(context.Background(), func(ctx context.Context) error {
			events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 3)
			if err != nil {
				first <- nil
				close(firstClaimed)
				return err
			}
			first <- idsOf(events)
			close(firstClaimed)
			// Hold the locks until the second claim has run.
			<-second
			return nil
		})
	}()

	<-firstClaimed
	firstIDs := <-first

	err := repo.WithTx(context.Background(), func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 3)
		if err != nil {
			return err
		}
		secondIDs := idsOf(events)

		for _, a := range firstIDs {
			for _, b := range secondIDs {
				if a == b {
					t.Errorf("event %d was claimed by both drainers — it would be published twice", a)
				}
			}
		}
		if len(secondIDs) != 3 {
			t.Errorf("second drainer claimed %d events, want 3 (it should skip locked rows, not block)", len(secondIDs))
		}
		return nil
	})
	close(second)
	if err != nil {
		t.Fatalf("second WithTx: %v", err)
	}
}

func idsOf(events []*dao.OutboxEvent) []int64 {
	out := make([]int64, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func TestMarkPublishedRemovesFromTheQueue(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	saveEvents(t, repo, 2)
	ctx := context.Background()

	err := repo.WithTx(ctx, func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 10)
		if err != nil {
			return err
		}
		return repo.MarkOutboxEventPublished(ctx, events[0].ID)
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	err = repo.WithTx(ctx, func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 10)
		if err != nil {
			return err
		}
		if len(events) != 1 {
			t.Errorf("unpublished events = %d, want 1", len(events))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// A failed publish must leave the event queued, with the reason visible.
func TestMarkFailedKeepsEventQueued(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	saveEvents(t, repo, 1)
	ctx := context.Background()

	err := repo.WithTx(ctx, func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 10)
		if err != nil {
			return err
		}
		return repo.MarkOutboxEventFailed(ctx, events[0].ID, "broker unreachable")
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	err = repo.WithTx(ctx, func(ctx context.Context) error {
		events, err := repo.ClaimUnpublishedOutboxEvents(ctx, 10)
		if err != nil {
			return err
		}
		if len(events) != 1 {
			t.Fatalf("unpublished events = %d, want 1 — a failed publish lost the event", len(events))
		}
		if events[0].Attempts != 1 {
			t.Errorf("attempts = %d, want 1", events[0].Attempts)
		}
		if events[0].LastError != "broker unreachable" {
			t.Errorf("last_error = %q, want the recorded cause", events[0].LastError)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// Consumers dedupe on event_id, so duplicates at the source must be impossible.
func TestEventIDIsUnique(t *testing.T) {
	repo := testRepository(t)
	truncateOutbox(t, repo)
	ctx := context.Background()

	event := newEvent("wallet.credited", "wlt_1")
	if err := repo.SaveOutboxEvent(ctx, event); err != nil {
		t.Fatalf("first save: %v", err)
	}

	duplicate := newEvent("wallet.credited", "wlt_1")
	duplicate.EventID = event.EventID
	if err := repo.SaveOutboxEvent(ctx, duplicate); err == nil {
		t.Error("saving a duplicate event_id should fail")
	}
}
