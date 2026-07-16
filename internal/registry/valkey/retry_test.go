package valkey

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryConnectSucceedsAfterTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	want := &Store{}
	store, err := RetryConnect(context.Background(), 5*time.Second, time.Millisecond, 10*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("dial error: connection refused")
			}
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, store)
	require.EqualValues(t, 3, attempts.Load())
}

func TestRetryConnectImmediateSuccess(t *testing.T) {
	var attempts atomic.Int32
	want := &Store{}
	store, err := RetryConnect(context.Background(), 5*time.Second, time.Millisecond, 10*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, store)
	require.EqualValues(t, 1, attempts.Load())
}

func TestRetryConnectWindowExhausted(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	start := time.Now()
	store, err := RetryConnect(context.Background(), 80*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	elapsed := time.Since(start)
	require.Nil(t, store)
	require.ErrorIs(t, err, connectErr)
	require.Greater(t, attempts.Load(), int32(1), "must retry within the window")
	require.Less(t, elapsed, 2*time.Second, "window is a hard ceiling")
}

func TestRetryConnectZeroWindowSingleAttempt(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	store, err := RetryConnect(context.Background(), 0, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	require.Nil(t, store)
	require.ErrorIs(t, err, connectErr)
	require.EqualValues(t, 1, attempts.Load())
}

func TestRetryConnectCtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	store, err := RetryConnect(ctx, 10*time.Second, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			return nil, errors.New("dial error: connection refused")
		})
	require.Nil(t, store)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second, "must abort promptly on cancel")
}

func TestRetryConnectCtxCanceledBySignalCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("terminated signal received"))
	var attempts atomic.Int32
	store, err := RetryConnect(ctx, 10*time.Second, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			return nil, errors.New("dial error: connection refused")
		})
	require.Nil(t, store)
	require.ErrorIs(t, err, context.Canceled,
		"shutdown must surface as context.Canceled regardless of cancel cause, so main() can route it away from Sentry")
	require.LessOrEqual(t, attempts.Load(), int32(1))
}

func TestRetryConnectLateSuccessBeatsDeadlineCheck(t *testing.T) {
	want := &Store{}
	store, err := RetryConnect(context.Background(), 5*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			return want, nil
		})
	require.NoError(t, err, "a successful connect must never be discarded as a timeout")
	require.Same(t, want, store)
}

func TestNewWithRetryUnreachableBoundedByWindow(t *testing.T) {
	start := time.Now()
	store, err := NewWithRetry(context.Background(), Config{Addr: "127.0.0.1:1"}, 1200*time.Millisecond)
	require.Nil(t, store)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 3*time.Second, "window is a hard ceiling")
}
