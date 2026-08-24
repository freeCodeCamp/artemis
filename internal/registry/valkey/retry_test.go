package valkey

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func TestNewWithRetryBudgetsEachAttempt(t *testing.T) {
	p := policy(5 * time.Second)

	require.Equal(t, DialTimeout, p.Attempt,
		"a zero attempt budget hands the whole window to one dial; a hung dial then retries nothing")
	require.Equal(t, "valkey.connect.retrying", p.Event)
	require.Equal(t, 5*time.Second, p.Window)
	require.Equal(t, RetryBackoffBase, p.Base)
	require.Equal(t, RetryBackoffMax, p.Max)
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

func TestNewClientWithRetryPinsDialTimeout(t *testing.T) {
	mr := miniredis.RunT(t)
	client, err := NewClientWithRetry(context.Background(), Config{Addr: mr.Addr()}, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.Equal(t, DialTimeout, client.Options().DialTimeout,
		"the teamcache client is the call site that once built a raw redis.Options literal in main")
}

func TestNewWithRetryUnreachableBoundedByWindow(t *testing.T) {
	start := time.Now()
	store, err := NewWithRetry(context.Background(), Config{Addr: "127.0.0.1:1"}, 1200*time.Millisecond)
	require.Nil(t, store)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 3*time.Second, "window is a hard ceiling")
}
