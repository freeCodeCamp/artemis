package teamcache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCache(t *testing.T, ttl time.Duration) (*Cache, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return New(client, ttl), mr
}

func TestTeamCache(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestCache(t, 5*time.Minute)

	_, hit, err := c.Get(ctx, "alice")
	require.NoError(t, err)
	assert.False(t, hit, "cold cache misses")

	require.NoError(t, c.Set(ctx, "alice", []string{"staff", "team-eng"}))
	teams, hit, err := c.Get(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, []string{"staff", "team-eng"}, teams)
}

func TestTeamCache_GetOrFetch_FetchesOnceThenCaches(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestCache(t, 5*time.Minute)

	calls := 0
	fetch := func(context.Context) ([]string, error) {
		calls++
		return []string{"staff"}, nil
	}

	teams, err := c.GetOrFetch(ctx, "bob", fetch)
	require.NoError(t, err)
	assert.Equal(t, []string{"staff"}, teams)
	assert.Equal(t, 1, calls, "miss triggers exactly one upstream fetch")

	teams, err = c.GetOrFetch(ctx, "bob", fetch)
	require.NoError(t, err)
	assert.Equal(t, []string{"staff"}, teams)
	assert.Equal(t, 1, calls, "second call served from Valkey cache; GitHub App quota protected")
}

func TestTeamCache_CachesEmptyMembership(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestCache(t, 5*time.Minute)

	calls := 0
	fetch := func(context.Context) ([]string, error) {
		calls++
		return nil, nil
	}
	_, err := c.GetOrFetch(ctx, "outsider", fetch)
	require.NoError(t, err)

	teams, hit, err := c.Get(ctx, "outsider")
	require.NoError(t, err)
	assert.True(t, hit, "an empty team list is cached, not treated as a miss")
	assert.Empty(t, teams)

	_, err = c.GetOrFetch(ctx, "outsider", fetch)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "non-member result is cached too — no re-fetch storm")
}

func TestTeamCache_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)

	require.NoError(t, c.Set(ctx, "alice", []string{"staff"}))
	mr.FastForward(2 * time.Minute)

	_, hit, err := c.Get(ctx, "alice")
	require.NoError(t, err)
	assert.False(t, hit, "entry expires after TTL -> miss")
}

func TestTeamCache_FetchErrorNotCached(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestCache(t, time.Minute)

	_, err := c.GetOrFetch(ctx, "carol", func(context.Context) ([]string, error) {
		return nil, errors.New("github 503")
	})
	require.Error(t, err)

	_, hit, err := c.Get(ctx, "carol")
	require.NoError(t, err)
	assert.False(t, hit, "a failed upstream fetch is never cached")
}
