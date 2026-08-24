package retryconnect

import (
	"context"
	"log/slog"
	"time"
)

type Policy struct {
	Event   string
	Window  time.Duration
	Attempt time.Duration
	Base    time.Duration
	Max     time.Duration
}

func within[T any](ctx context.Context, deadline time.Time, attempt time.Duration, connect func(context.Context) (T, error)) (T, error) {
	budget := time.Until(deadline)
	if attempt > 0 && attempt < budget {
		budget = attempt
	}
	attemptCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return connect(attemptCtx)
}

func Do[T any](ctx context.Context, p Policy, connect func(context.Context) (T, error)) (T, error) {
	if p.Window <= 0 {
		return connect(ctx)
	}

	deadline := time.Now().Add(p.Window)

	backoff := p.Base
	for n := 1; ; n++ {
		v, err := within(ctx, deadline, p.Attempt, connect)
		if err == nil {
			return v, nil
		}
		if ctx.Err() != nil {
			var zero T
			return zero, ctx.Err()
		}
		if remaining := time.Until(deadline); remaining <= backoff {
			var zero T
			return zero, err
		}
		slog.Warn(p.Event,
			"attempt", n,
			"backoff", backoff,
			"err", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			var zero T
			return zero, ctx.Err()
		case <-timer.C:
		}
		if backoff *= 2; backoff > p.Max {
			backoff = p.Max
		}
	}
}
