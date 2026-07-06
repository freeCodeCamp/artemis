package pg

import (
	"context"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
)

const (
	retryBackoffBase = 500 * time.Millisecond
	retryBackoffMax  = 5 * time.Second
)

func NewWithRetry(ctx context.Context, cfg Config, window time.Duration) (*DB, error) {
	return retryConnect(ctx, window, retryBackoffBase, retryBackoffMax,
		func(ctx context.Context) (*DB, error) {
			return New(ctx, cfg)
		})
}

func retryConnect(ctx context.Context, window, base, max time.Duration, connect func(context.Context) (*DB, error)) (*DB, error) {
	if window <= 0 {
		return connect(ctx)
	}

	deadline := time.Now().Add(window)
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	backoff := base
	for attempt := 1; ; attempt++ {
		db, err := connect(attemptCtx)
		if err == nil {
			return db, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if remaining := time.Until(deadline); remaining <= backoff {
			return nil, err
		}
		slog.Warn("pg: connect failed, retrying",
			"attempt", attempt,
			"backoff", backoff,
			"err", err,
			"reqID", telemetry.FromContext(ctx).ReqID)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff *= 2; backoff > max {
			backoff = max
		}
	}
}
