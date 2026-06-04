package teamcache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func failCommands(t *testing.T, mr *miniredis.Miniredis, msg string, cmds ...string) {
	t.Helper()
	fail := make(map[string]struct{}, len(cmds))
	for _, c := range cmds {
		fail[c] = struct{}{}
	}
	mr.Server().SetPreHook(func(c *server.Peer, cmd string, args ...string) bool {
		if _, ok := fail[cmd]; ok {
			c.WriteError(msg)
			return true
		}
		return false
	})
	t.Cleanup(func() { mr.Server().SetPreHook(nil) })
}

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

func TestTeamCache_Get_MalformedJSONIsAnError(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)
	require.NoError(t, mr.Set("ghteams:eve", "not-json"))

	teams, hit, err := c.Get(ctx, "eve")
	require.Error(t, err)
	assert.False(t, hit, "a poisoned cache value must not read as a hit")
	assert.Nil(t, teams, "a decode failure must not leak a partial team list")
	assert.ErrorContains(t, err, "teamcache decode")
}

func TestTeamCache_Get_RedisErrorPropagates(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)
	failCommands(t, mr, "LOADING Redis is loading the dataset in memory", "GET")

	teams, hit, err := c.Get(ctx, "alice")
	require.Error(t, err)
	assert.False(t, hit, "a backend error must not read as a hit")
	assert.Nil(t, teams)
	assert.ErrorContains(t, err, "teamcache get")
}

func TestTeamCache_GetOrFetch_SetFailurePropagates(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)
	failCommands(t, mr, "READONLY You can't write against a read only replica.", "SET")

	calls := 0
	teams, err := c.GetOrFetch(ctx, "bob", func(context.Context) ([]string, error) {
		calls++
		return []string{"staff"}, nil
	})
	require.Error(t, err, "an unpersisted fetch must surface the write error, not pose as cached")
	assert.Nil(t, teams)
	assert.Equal(t, 1, calls, "the miss path runs fetch before the failing Set")
	assert.ErrorContains(t, err, "teamcache set")
}
