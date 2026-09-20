package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
)

// fakeRepo is an in-memory stand-in. The real storage semantics are covered by
// the postgresql integration tests; these tests are about the drainer's own
// decisions.
type fakeRepo struct {
	mu        sync.Mutex
	pending   []*dao.OutboxEvent
	published []int64
	failed    map[int64]string
}

func newFakeRepo(events ...*dao.OutboxEvent) *fakeRepo {
	return &fakeRepo{pending: events, failed: map[int64]string{}}
}

func (f *fakeRepo) WithTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *fakeRepo) ClaimUnpublishedOutboxEvents(ctx context.Context, limit int) ([]*dao.OutboxEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) < limit {
		limit = len(f.pending)
	}
	claimed := f.pending[:limit]
	f.pending = f.pending[limit:]
	return claimed, nil
}

func (f *fakeRepo) MarkOutboxEventPublished(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, id)
	return nil
}

func (f *fakeRepo) MarkOutboxEventFailed(ctx context.Context, id int64, cause string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[id] = cause
	return nil
}

type fakePublisher struct {
	mu        sync.Mutex
	published []string
	failOn    map[string]error
}

func (p *fakePublisher) Name() string { return "fake" }

func (p *fakePublisher) Publish(ctx context.Context, event *dao.OutboxEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err, ok := p.failOn[event.EventID]; ok {
		return err
	}
	p.published = append(p.published, event.EventID)
	return nil
}

func events(n int) []*dao.OutboxEvent {
	out := make([]*dao.OutboxEvent, n)
	for i := range out {
		out[i] = &dao.OutboxEvent{ID: int64(i + 1), EventID: string(rune('a'+i)) + "-evt", Topic: "t"}
	}
	return out
}

func TestDrainOncePublishesBatch(t *testing.T) {
	repo := newFakeRepo(events(3)...)
	pub := &fakePublisher{failOn: map[string]error{}}
	d := NewDrainer(Config{BatchSize: 10}, repo, pub)

	handled, err := d.drainOnce(context.Background())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if handled != 3 {
		t.Errorf("handled = %d, want 3", handled)
	}
	if len(pub.published) != 3 {
		t.Errorf("published %d events, want 3", len(pub.published))
	}
	if len(repo.published) != 3 {
		t.Errorf("marked %d published, want 3", len(repo.published))
	}
}

// One undeliverable event must not strand the rest of the batch behind it.
func TestDrainOnceContinuesPastAFailure(t *testing.T) {
	all := events(3)
	repo := newFakeRepo(all...)
	pub := &fakePublisher{failOn: map[string]error{all[1].EventID: errors.New("broker unreachable")}}
	d := NewDrainer(Config{BatchSize: 10}, repo, pub)

	if _, err := d.drainOnce(context.Background()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(pub.published) != 2 {
		t.Errorf("published %d events, want the 2 that could be delivered", len(pub.published))
	}
	if len(repo.published) != 2 {
		t.Errorf("marked %d published, want 2", len(repo.published))
	}
	if cause, ok := repo.failed[all[1].ID]; !ok {
		t.Error("the failing event was not recorded as failed, so it would retry silently")
	} else if cause != "broker unreachable" {
		t.Errorf("recorded cause = %q, want the publisher's error", cause)
	}
	// It stays unpublished, so the next sweep retries it.
	for _, id := range repo.published {
		if id == all[1].ID {
			t.Error("a failed event must not be marked published")
		}
	}
}

func TestDrainOnceWithEmptyQueue(t *testing.T) {
	d := NewDrainer(Config{BatchSize: 10}, newFakeRepo(), &fakePublisher{failOn: map[string]error{}})

	handled, err := d.drainOnce(context.Background())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if handled != 0 {
		t.Errorf("handled = %d, want 0", handled)
	}
}

// A backlog larger than one batch must drain without waiting for the next tick.
func TestRunDrainsBacklogWithoutWaitingForTheTicker(t *testing.T) {
	repo := newFakeRepo(events(7)...)
	pub := &fakePublisher{failOn: map[string]error{}}
	// A long interval: if the drainer paced itself by the ticker, this test
	// would time out rather than finish.
	d := NewDrainer(Config{BatchSize: 2, Interval: time.Hour}, repo, pub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		pub.mu.Lock()
		n := len(pub.published)
		pub.mu.Unlock()
		if n == 7 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("published %d of 7 events; the backlog was not drained in one pass", n)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run did not return after its context was cancelled")
	}
}

func TestConfigDefaults(t *testing.T) {
	d := NewDrainer(Config{}, newFakeRepo(), &fakePublisher{})
	if d.cfg.Interval <= 0 {
		t.Error("Interval should get a default")
	}
	if d.cfg.BatchSize <= 0 {
		t.Error("BatchSize should get a default")
	}
}
