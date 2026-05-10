package valkey_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

// newMiniredis returns a miniredis server seeded with the given
// password. The server lifetime is bound to t.Cleanup so each test
// gets a fresh, isolated instance.
func newMiniredis(t *testing.T, password string) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	if password != "" {
		mr.RequireAuth(password)
	}
	return mr
}

// newStore boots a fresh miniredis + Store with a deterministic
// clock at 2026-01-01T00:00:00Z. Returned Now lets the test advance
// time between operations.
func newStore(t *testing.T) (*valkey.Store, *miniredis.Miniredis, func(time.Duration)) {
	t.Helper()
	mr := newMiniredis(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	return s, mr, advance
}

func TestNewStore_PingsValkey(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Ping(ctx))
}

func TestNewStore_AuthRequired(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "secret-pw")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr(), Password: "wrong"})
	require.Error(t, err)

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr(), Password: "secret-pw"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Ping(ctx))
}

func TestNewStore_RejectsEmptyAddr(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := valkey.New(ctx, valkey.Config{})
	require.Error(t, err)
}

func TestStore_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "")
	ctx := context.Background()

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_ = s.Close()
}

func TestStore_Register_HappyPath(t *testing.T) {
	t.Parallel()

	s, mr, _ := newStore(t)
	ctx := context.Background()

	got, err := s.Register(ctx, "blog", []string{"news-editors", "platform"}, "alice")
	require.NoError(t, err)
	require.Equal(t, "blog", got.Slug)
	require.Equal(t, []string{"news-editors", "platform"}, got.Teams)
	require.Equal(t, "alice", got.CreatedBy)
	require.False(t, got.CreatedAt.IsZero())
	require.True(t, got.CreatedAt.Equal(got.UpdatedAt))

	// Wire shape: HSET site:<slug>, SADD sites:all, schema fields
	// per RFC §B Schema.
	require.True(t, mr.Exists("site:blog"), "hash row missing")
	members, err := mr.SMembers("sites:all")
	require.NoError(t, err)
	require.Contains(t, members, "blog")
	require.Equal(t, `["news-editors","platform"]`, mr.HGet("site:blog", "teams"))
	require.Equal(t, "alice", mr.HGet("site:blog", "created_by"))
}

func TestStore_Register_AlreadyExistsOnDuplicate(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	_, err := s.Register(ctx, "blog", []string{"staff"}, "alice")
	require.NoError(t, err)

	_, err = s.Register(ctx, "blog", []string{"other-team"}, "mallory")
	require.ErrorIs(t, err, valkey.ErrAlreadyExists)
}

func TestStore_Register_PublishesRegistryChanged(t *testing.T) {
	t.Parallel()

	s, mr, _ := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscribe directly via a separate go-redis client; miniredis
	// supports pub-sub natively.
	sub := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = sub.Close() })
	pub := sub.Subscribe(ctx, valkey.ChannelRegistryChanged)
	t.Cleanup(func() { _ = pub.Close() })
	// Drain the subscribe-confirmation message before the assertion.
	_, err := pub.Receive(ctx)
	require.NoError(t, err)

	_, err = s.Register(ctx, "blog", []string{"staff"}, "alice")
	require.NoError(t, err)

	msg, err := pub.ReceiveMessage(ctx)
	require.NoError(t, err)
	require.Equal(t, valkey.ChannelRegistryChanged, msg.Channel)
	require.Equal(t, "blog", msg.Payload)
}

func TestStore_Register_RejectsEmptySlug(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	_, err := s.Register(ctx, "", []string{"staff"}, "alice")
	require.Error(t, err)
}

func TestStore_Register_ConcurrentSerializesToOneSuccess(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	const goroutines = 10
	var (
		ok    atomic.Int32
		dup   atomic.Int32
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Register(ctx, "blog", []string{"staff"}, "alice")
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, valkey.ErrAlreadyExists):
				dup.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), ok.Load(), "exactly one register must win")
	require.Equal(t, int32(goroutines-1), dup.Load(), "all losers see ErrAlreadyExists")
}

func TestStore_TeamsForSite_HitAndMiss(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	_, err := s.Register(ctx, "blog", []string{"news-editors"}, "alice")
	require.NoError(t, err)

	teams, err := s.TeamsForSite(ctx, "blog")
	require.NoError(t, err)
	require.Equal(t, []string{"news-editors"}, teams)

	_, err = s.TeamsForSite(ctx, "absent")
	require.ErrorIs(t, err, valkey.ErrNotFound)
}

func TestStore_GetSite_RoundTripsTimestamps(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	original, err := s.Register(ctx, "blog", []string{"staff"}, "alice")
	require.NoError(t, err)

	got, err := s.GetSite(ctx, "blog")
	require.NoError(t, err)
	require.True(t, got.CreatedAt.Equal(original.CreatedAt), "created_at round-trip")
	require.True(t, got.UpdatedAt.Equal(original.UpdatedAt), "updated_at round-trip")
}

func TestStore_Sites_EnumeratesSorted(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	for _, slug := range []string{"charlie", "alpha", "bravo"} {
		_, err := s.Register(ctx, slug, []string{"staff"}, "alice")
		require.NoError(t, err)
	}

	all, err := s.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, "alpha", all[0].Slug)
	require.Equal(t, "bravo", all[1].Slug)
	require.Equal(t, "charlie", all[2].Slug)
}

func TestStore_Sites_EmptyWhenUnregistered(t *testing.T) {
	t.Parallel()

	s, _, _ := newStore(t)
	ctx := context.Background()

	all, err := s.Sites(ctx)
	require.NoError(t, err)
	require.Empty(t, all)
}
