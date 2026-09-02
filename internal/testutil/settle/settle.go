package settle

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Observation func(context.Context) (bool, error)

type Option func(*config)

type config struct {
	every       time.Duration
	consecutive int
	perAttempt  time.Duration
}

var ErrBudgetExpired = errors.New("settle: budget expired")

func Every(d time.Duration) Option { return func(c *config) { c.every = d } }

func Consecutive(n int) Option { return func(c *config) { c.consecutive = n } }

func PerAttempt(d time.Duration) Option { return func(c *config) { c.perAttempt = d } }

func Until(ctx context.Context, budget time.Duration, observe Observation, opts ...Option) error {
	cfg := config{every: 250 * time.Millisecond, consecutive: 1}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.consecutive < 1 {
		cfg.consecutive = 1
	}
	deadline := time.Now().Add(budget)
	attempts, streak := 0, 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return cancelled(err, lastErr)
		}
		if attempts > 0 && !time.Now().Before(deadline) {
			break
		}
		ok, err := observeOnce(ctx, cfg.perAttempt, observe)
		attempts++
		switch {
		case err != nil:
			lastErr = err
			streak = 0
		case ok:
			lastErr = nil
			streak++
			if streak >= cfg.consecutive {
				return nil
			}
		default:
			lastErr = nil
			streak = 0
		}
		if !time.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(cfg.every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return cancelled(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
	if lastErr != nil {
		return fmt.Errorf("%w after %d attempts in %s: %w", ErrBudgetExpired, attempts, budget, lastErr)
	}
	return fmt.Errorf("%w after %d attempts in %s", ErrBudgetExpired, attempts, budget)
}

func observeOnce(ctx context.Context, perAttempt time.Duration, observe Observation) (bool, error) {
	if perAttempt <= 0 {
		return observe(ctx)
	}
	actx, cancel := context.WithTimeout(ctx, perAttempt)
	defer cancel()
	return observe(actx)
}

func cancelled(ctxErr, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("settle: %w (last observation: %w)", ctxErr, lastErr)
	}
	return fmt.Errorf("settle: %w", ctxErr)
}
