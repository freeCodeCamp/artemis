package pg

import (
	"context"
	"time"

	"github.com/freeCodeCamp/artemis/internal/retryconnect"
)

const (
	retryBackoffBase = 500 * time.Millisecond
	retryBackoffMax  = 5 * time.Second
)

func connectPolicy(window time.Duration) retryconnect.Policy {
	return retryconnect.Policy{
		Event:   "pg.connect.retrying",
		Window:  window,
		Attempt: ConnectTimeout,
		Base:    retryBackoffBase,
		Max:     retryBackoffMax,
	}
}

func NewWithRetry(ctx context.Context, cfg Config, window time.Duration) (*DB, error) {
	return retryconnect.Do(ctx, connectPolicy(window),
		func(ctx context.Context) (*DB, error) {
			return New(ctx, cfg)
		})
}
