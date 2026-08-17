package teamcache

import (
	"context"
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

func TestTeamCache_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)

	require.NoError(t, c.Set(ctx, "alice", []string{"staff"}))
	mr.FastForward(2 * time.Minute)

	_, hit, err := c.Get(ctx, "alice")
	require.NoError(t, err)
	assert.False(t, hit, "entry expires after TTL -> miss")
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
