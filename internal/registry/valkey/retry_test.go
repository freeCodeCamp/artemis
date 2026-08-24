package valkey

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func TestRetryConnectSucceedsAfterTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	want := &Store{}
	store, err := RetryConnect(context.Background(), 5*time.Second, 0, time.Millisecond, 10*time.Millisecond,
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
	store, err := RetryConnect(context.Background(), 5*time.Second, 0, time.Millisecond, 10*time.Millisecond,
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
	store, err := RetryConnect(context.Background(), 80*time.Millisecond, 0, 10*time.Millisecond, 20*time.Millisecond,
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
	store, err := RetryConnect(context.Background(), 0, 0, 10*time.Millisecond, 20*time.Millisecond,
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
	store, err := RetryConnect(ctx, 10*time.Second, 0, 10*time.Millisecond, 20*time.Millisecond,
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
	store, err := RetryConnect(ctx, 10*time.Second, 0, 10*time.Millisecond, 20*time.Millisecond,
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
	store, err := RetryConnect(context.Background(), 5*time.Millisecond, 0, 100*time.Millisecond, 200*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			return want, nil
		})
	require.NoError(t, err, "a successful connect must never be discarded as a timeout")
	require.Same(t, want, store)
}

func TestRetryConnectBlockedAttemptDoesNotConsumeTheWindow(t *testing.T) {
	var attempts atomic.Int32
	store, err := RetryConnect(context.Background(), 200*time.Millisecond, 20*time.Millisecond, time.Millisecond, 5*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	require.Nil(t, store)
	require.Error(t, err)
	require.Greater(t, attempts.Load(), int32(1),
		"one blocked attempt must not swallow the whole window; a dial that hangs to its own timeout still leaves budget to retry")
}

func TestRetryConnectZeroAttemptFallsBackToWindow(t *testing.T) {
	var attempts atomic.Int32
	var seen error
	want := &Store{}
	store, err := RetryConnect(context.Background(), time.Second, 0, time.Millisecond, 5*time.Millisecond,
		func(ctx context.Context) (*Store, error) {
			attempts.Add(1)
			if err := ctx.Err(); err != nil {
				seen = err
				return nil, err
			}
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, store)
	require.EqualValues(t, 1, attempts.Load())
	require.NoError(t, seen,
		"a zero attempt budget must fall back to the whole remaining window, never to a context born expired")
}

func TestClientOptionsPinsDialTimeout(t *testing.T) {
	opts := ClientOptions(Config{Addr: "127.0.0.1:6379", Password: "pw"})
	require.Equal(t, DialTimeout, opts.DialTimeout,
		"go-redis defaults DialTimeout to 5s, which is the whole retry window — one hung dial would consume it")
	require.Equal(t, "127.0.0.1:6379", opts.Addr)
	require.Equal(t, "pw", opts.Password)
}

func TestNewPinsDialTimeout(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := New(context.Background(), Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.Equal(t, DialTimeout, store.client.Options().DialTimeout,
		"the live client must reach the dial budget through ClientOptions, like every other call site")
}

func TestNewWithRetryUnreachableBoundedByWindow(t *testing.T) {
	start := time.Now()
	store, err := NewWithRetry(context.Background(), Config{Addr: "127.0.0.1:1"}, 1200*time.Millisecond)
	require.Nil(t, store)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 3*time.Second, "window is a hard ceiling")
}
