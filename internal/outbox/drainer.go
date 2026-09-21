package outbox

import (
	"context"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"

	"github.com/gofreego/goutils/logger"
)

// Repository is the slice of storage the drainer needs.
type Repository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	ClaimUnpublishedOutboxEvents(ctx context.Context, limit int) ([]*dao.OutboxEvent, error)
	MarkOutboxEventPublished(ctx context.Context, id int64) error
	MarkOutboxEventFailed(ctx context.Context, id int64, cause string) error
}

type Config struct {
	// Interval between sweeps. The drainer also runs immediately on start.
	Interval time.Duration `yaml:"Interval"`
	// BatchSize caps how many events one sweep claims.
	BatchSize int `yaml:"BatchSize"`
}

func (c *Config) withDefaults() {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
}

// Drainer publishes unpublished outbox events on a schedule.
type Drainer struct {
	cfg       Config
	repo      Repository
	publisher Publisher
	metrics   *metrics
}

func NewDrainer(cfg Config, repo Repository, publisher Publisher) *Drainer {
	cfg.withDefaults()
	return &Drainer{
		cfg:       cfg,
		repo:      repo,
		publisher: publisher,
		metrics:   newMetrics(context.Background()),
	}
}

// Run sweeps until the context is cancelled.
func (d *Drainer) Run(ctx context.Context) {
	logger.Info(ctx, "outbox drainer started: publisher=%s interval=%s batch=%d",
		d.publisher.Name(), d.cfg.Interval, d.cfg.BatchSize)

	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	for {
		// Drain fully before waiting, so a backlog is not paced by the ticker.
		for {
			published, err := d.drainOnce(ctx)
			if err != nil {
				logger.Error(ctx, "outbox drain failed: %v", err)
				break
			}
			if published < d.cfg.BatchSize {
				break
			}
		}

		select {
		case <-ctx.Done():
			logger.Info(ctx, "outbox drainer stopped")
			return
		case <-ticker.C:
		}
	}
}

// drainOnce claims a batch and publishes it, returning how many events it
// handled. The claim, the publishing and the bookkeeping all happen inside one
// transaction so the row locks (FOR UPDATE SKIP LOCKED) hold for the whole
// sweep and a second drainer cannot pick up the same events.
func (d *Drainer) drainOnce(ctx context.Context) (int, error) {
	var handled int

	err := d.repo.WithTx(ctx, func(ctx context.Context) error {
		events, err := d.repo.ClaimUnpublishedOutboxEvents(ctx, d.cfg.BatchSize)
		if err != nil {
			return err
		}
		handled = len(events)

		for _, event := range events {
			if err := d.publisher.Publish(ctx, event); err != nil {
				// One bad event must not strand the rest of the batch, so the
				// failure is recorded and the sweep continues. The event stays
				// unpublished and is retried next time.
				logger.Error(ctx, "failed to publish outbox event %s: %v", event.EventID, err)
				d.metrics.recordFailure(ctx, event.Topic)
				if markErr := d.repo.MarkOutboxEventFailed(ctx, event.ID, err.Error()); markErr != nil {
					return markErr
				}
				continue
			}
			if err := d.repo.MarkOutboxEventPublished(ctx, event.ID); err != nil {
				return err
			}
			d.metrics.recordPublished(ctx, event.Topic, time.Since(event.CreatedAt).Seconds())
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return handled, nil
}
