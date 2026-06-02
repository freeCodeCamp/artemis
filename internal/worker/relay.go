package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

type OutboxSource interface {
	FetchUnpublished(ctx context.Context, limit int) ([]pg.OutboxEvent, error)
	MarkPublished(ctx context.Context, ids []int64, at time.Time) error
}

type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

type Relay struct {
	Source    OutboxSource
	Publisher Publisher
	Batch     int
	Now       func() time.Time
}

func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = 100
	}
	events, err := r.Source.FetchUnpublished(ctx, batch)
	if err != nil {
		return 0, fmt.Errorf("relay: fetch: %w", err)
	}

	var done []int64
	for _, e := range events {
		if err := r.Publisher.Publish(ctx, e.Topic, e.Payload); err != nil {
			r.mark(ctx, done)
			return len(done), fmt.Errorf("relay: publish id=%d topic=%s: %w", e.ID, e.Topic, err)
		}
		done = append(done, e.ID)
	}
	if err := r.mark(ctx, done); err != nil {
		return len(done), err
	}
	return len(done), nil
}

func (r *Relay) mark(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if err := r.Source.MarkPublished(ctx, ids, now()); err != nil {
		return fmt.Errorf("relay: mark published: %w", err)
	}
	return nil
}
