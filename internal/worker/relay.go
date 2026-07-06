package worker

import (
	"context"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

type OutboxSource interface {
	RelayBatch(ctx context.Context, limit int, publish func(pg.OutboxEvent) error, at time.Time) (int, error)
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
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return r.Source.RelayBatch(ctx, batch, func(e pg.OutboxEvent) error {
		return r.Publisher.Publish(ctx, e.Topic, e.Payload)
	}, now())
}
