package retryconnect

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDoSucceedsAfterTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	want := new(int)
	got, err := Do(context.Background(), Policy{Window: 5 * time.Second, Base: time.Millisecond, Max: 10 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("dial error: connection refused")
			}
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, got)
	require.EqualValues(t, 3, attempts.Load())
}

func TestDoImmediateSuccess(t *testing.T) {
	var attempts atomic.Int32
	want := new(int)
	got, err := Do(context.Background(), Policy{Window: 5 * time.Second, Base: time.Millisecond, Max: 10 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, got)
	require.EqualValues(t, 1, attempts.Load())
}

func TestDoWindowExhausted(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	start := time.Now()
	got, err := Do(context.Background(), Policy{Window: 80 * time.Millisecond, Base: 10 * time.Millisecond, Max: 20 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	elapsed := time.Since(start)
	require.Nil(t, got)
	require.ErrorIs(t, err, connectErr)
	require.Greater(t, attempts.Load(), int32(1), "must retry within the window")
	require.Less(t, elapsed, 2*time.Second, "window is a hard ceiling")
}

func TestDoZeroWindowSingleAttempt(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	got, err := Do(context.Background(), Policy{Base: 10 * time.Millisecond, Max: 20 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	require.Nil(t, got)
	require.ErrorIs(t, err, connectErr)
	require.EqualValues(t, 1, attempts.Load())
}

func TestDoCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	got, err := Do(ctx, Policy{Window: 10 * time.Second, Base: 10 * time.Millisecond, Max: 20 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			return nil, errors.New("dial error: connection refused")
		})
	require.Nil(t, got)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second, "must abort promptly on cancel")
}

func TestDoCtxCanceledBySignalCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("terminated signal received"))
	var attempts atomic.Int32
	got, err := Do(ctx, Policy{Window: 10 * time.Second, Base: 10 * time.Millisecond, Max: 20 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			return nil, errors.New("dial error: connection refused")
		})
	require.Nil(t, got)
	require.ErrorIs(t, err, context.Canceled,
		"shutdown must surface as context.Canceled regardless of cancel cause, so main() can route it away from Sentry")
	require.LessOrEqual(t, attempts.Load(), int32(1))
}

func TestDoLateSuccessBeatsDeadlineCheck(t *testing.T) {
	want := new(int)
	got, err := Do(context.Background(), Policy{Window: 5 * time.Millisecond, Base: 100 * time.Millisecond, Max: 200 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			return want, nil
		})
	require.NoError(t, err, "a successful connect must never be discarded as a timeout")
	require.Same(t, want, got)
}

func TestDoBlockedAttemptDoesNotConsumeTheWindow(t *testing.T) {
	var attempts atomic.Int32
	got, err := Do(context.Background(), Policy{Window: 200 * time.Millisecond, Attempt: 20 * time.Millisecond, Base: time.Millisecond, Max: 5 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	require.Nil(t, got)
	require.Error(t, err)
	require.Greater(t, attempts.Load(), int32(1),
		"one blocked attempt must not swallow the whole window; a dial that hangs to its own timeout still leaves budget to retry")
}

func TestDoZeroAttemptFallsBackToWindow(t *testing.T) {
	var attempts atomic.Int32
	var seen error
	want := new(int)
	got, err := Do(context.Background(), Policy{Window: time.Second, Base: time.Millisecond, Max: 5 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			attempts.Add(1)
			if err := ctx.Err(); err != nil {
				seen = err
				return nil, err
			}
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, got)
	require.EqualValues(t, 1, attempts.Load())
	require.NoError(t, seen,
		"a zero attempt budget must fall back to the whole remaining window, never to a context born expired")
}

func TestDoLogsTheCallerEvent(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	_, err := Do(context.Background(), Policy{Event: "pg.connect.retrying", Window: 100 * time.Millisecond, Base: time.Millisecond, Max: 5 * time.Millisecond},
		func(ctx context.Context) (*int, error) {
			return nil, errors.New("dial error: connection refused")
		})

	require.Error(t, err)
	require.Contains(t, buf.String(), "pg.connect.retrying",
		"the caller's event name is the Loki query key; a shared loop must not rename it")
	require.NotContains(t, buf.String(), "valkey.connect.retrying")
}
