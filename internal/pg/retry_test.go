package pg

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
	want := &DB{}
	db, err := retryConnect(context.Background(), 5*time.Second, time.Millisecond, 10*time.Millisecond,
		func(ctx context.Context) (*DB, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("dial error: connection refused")
			}
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, db)
	require.EqualValues(t, 3, attempts.Load())
}

func TestRetryConnectImmediateSuccess(t *testing.T) {
	var attempts atomic.Int32
	want := &DB{}
	db, err := retryConnect(context.Background(), 5*time.Second, time.Millisecond, 10*time.Millisecond,
		func(ctx context.Context) (*DB, error) {
			attempts.Add(1)
			return want, nil
		})
	require.NoError(t, err)
	require.Same(t, want, db)
	require.EqualValues(t, 1, attempts.Load())
}

func TestRetryConnectWindowExhausted(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	start := time.Now()
	db, err := retryConnect(context.Background(), 80*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*DB, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	elapsed := time.Since(start)
	require.Nil(t, db)
	require.ErrorIs(t, err, connectErr)
	require.Greater(t, attempts.Load(), int32(1), "must retry within the window")
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "window minus final-backoff slack")
	require.Less(t, elapsed, 2*time.Second, "window is a hard ceiling")
}

func TestRetryConnectZeroWindowSingleAttempt(t *testing.T) {
	var attempts atomic.Int32
	connectErr := errors.New("dial error: connection refused")
	db, err := retryConnect(context.Background(), 0, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*DB, error) {
			attempts.Add(1)
			return nil, connectErr
		})
	require.Nil(t, db)
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
	db, err := retryConnect(ctx, 10*time.Second, 10*time.Millisecond, 20*time.Millisecond,
		func(ctx context.Context) (*DB, error) {
			return nil, errors.New("dial error: connection refused")
		})
	require.Nil(t, db)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second, "must abort promptly on cancel")
}

func TestNewWithRetryUnreachableRetriesWithinWindow(t *testing.T) {
	start := time.Now()
	db, err := NewWithRetry(context.Background(), Config{
		DatabaseURL: "postgres://artemis:x@127.0.0.1:1/artemis?sslmode=disable&connect_timeout=1",
	}, 1200*time.Millisecond)
	elapsed := time.Since(start)
	require.Nil(t, db)
	require.Error(t, err)
	require.GreaterOrEqual(t, elapsed, 450*time.Millisecond, "at least one real backoff cycle")
	require.Less(t, elapsed, 3*time.Second, "window is a hard ceiling")
}
