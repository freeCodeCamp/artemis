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

const (
	DefaultPublishTimeout    = 10 * time.Second
	DefaultRelayBatchTimeout = 2 * time.Minute
	defaultRelayBatch        = 100
)

type Relay struct {
	Source         OutboxSource
	Publisher      Publisher
	Batch          int
	PublishTimeout time.Duration
	BatchTimeout   time.Duration
	Now            func() time.Time
}

func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = defaultRelayBatch
	}
	perPublish := r.PublishTimeout
	if perPublish <= 0 {
		perPublish = DefaultPublishTimeout
	}
	batchTimeout := r.BatchTimeout
	if batchTimeout <= 0 {
		batchTimeout = DefaultRelayBatchTimeout
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	return r.Source.RelayBatch(ctx, batch, func(e pg.OutboxEvent) error {
		pctx, pcancel := context.WithTimeout(ctx, perPublish)
		defer pcancel()
		return r.Publisher.Publish(pctx, e.Topic, e.Payload)
	}, now())
}
