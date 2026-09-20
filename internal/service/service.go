package service

import (
	"context"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/models/dao"
)

type Config struct {
}

type Repository interface {
	Ping(ctx context.Context) error

	// WithTx runs fn inside a single database transaction, committing when fn
	// returns nil and rolling back on error or panic. The transaction travels on
	// the context, so every repository call made with that ctx joins it.
	//
	// Nested calls join the outer transaction rather than opening a new one, so
	// a service method that calls another still forms one unit of work.
	//
	// Anything that moves money runs inside this. A ledger journal and its
	// postings and balance updates commit together or not at all.
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error

	IdempotencyRepository
	OutboxRepository
}

// IdempotencyRepository backs retry-safe mutations (plan.md D4).
//
// The intended sequence is:
//
//	ClaimIdempotencyKey        — on its own, committed immediately so concurrent
//	                             duplicates can see the claim
//	WithTx( work; CompleteIdempotencyKey )
//	                           — response recorded in the same commit as the work,
//	                             so a stored response always implies the work happened
//	ReleaseIdempotencyKey      — on failure, so an immediate retry can re-run
type IdempotencyRepository interface {
	// ClaimIdempotencyKey attempts to reserve (scope, key). It returns the
	// existing record when someone else got there first, and claimed=false.
	// The insert-or-nothing must be atomic: two concurrent identical requests
	// must not both come back claimed.
	ClaimIdempotencyKey(ctx context.Context, key *dao.IdempotencyKey) (existing *dao.IdempotencyKey, claimed bool, err error)

	// CompleteIdempotencyKey records the response. Call it inside the same
	// transaction as the work, so the two commit together.
	CompleteIdempotencyKey(ctx context.Context, scope, key string, response []byte) error

	// ReleaseIdempotencyKey drops an in-progress claim so a retry can re-run.
	// Only failed work releases: successful work has already recorded a response.
	ReleaseIdempotencyKey(ctx context.Context, scope, key string) error

	// DeleteExpiredIdempotencyKeys prunes the table and frees keys abandoned by
	// a crashed process. Returns how many rows went.
	DeleteExpiredIdempotencyKeys(ctx context.Context, limit int) (int64, error)
}

// OutboxRepository backs reliable event publishing (plan.md D5).
type OutboxRepository interface {
	// SaveOutboxEvent records an event. Call it inside the transaction that
	// makes the change it describes, never outside.
	SaveOutboxEvent(ctx context.Context, event *dao.OutboxEvent) error

	// ClaimUnpublishedOutboxEvents locks a batch of unpublished events for this
	// worker, skipping rows another worker holds, and returns them oldest first.
	// Must be called inside a transaction: the locks live until it ends.
	ClaimUnpublishedOutboxEvents(ctx context.Context, limit int) ([]*dao.OutboxEvent, error)

	// MarkOutboxEventPublished stamps an event as delivered.
	MarkOutboxEventPublished(ctx context.Context, id int64) error

	// MarkOutboxEventFailed records a delivery attempt that did not succeed, so
	// a stuck event is visible rather than silently retried forever.
	MarkOutboxEventFailed(ctx context.Context, id int64, cause string) error
}

type Service struct {
	repo Repository
	openpay_v1.UnimplementedOpenPayServer
}

func NewService(ctx context.Context, cfg *Config, repo Repository) *Service {
	return &Service{
		repo: repo,
	}
}
