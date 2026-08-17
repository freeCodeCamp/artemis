package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGitHubClient_CachesAreBoundedAcrossTokenChurn(t *testing.T) {
	clock := time.Unix(0, 0)
	c := NewGitHubClient(GitHubClientConfig{Now: func() time.Time { return clock }})

	for i := 0; i < maxCacheEntries*2; i++ {
		c.cacheNegative(fmt.Sprintf("hash-%d", i), ErrGitHubUnauthenticated)
	}

	assert.LessOrEqual(t, len(c.userCache), maxCacheEntries,
		"every distinct bearer ever presented used to leave a permanent map entry; CI tokens rotate and "+
			"pods live for weeks, so the maps grew monotonically for the life of the process")

	c.mu.Lock()
	for i := 0; i < maxCacheEntries*2; i++ {
		key := teamCacheKey{user: fmt.Sprintf("u-%d", i), team: "t"}
		pruneMap(c.teamCache, func(e teamCacheEntry) bool { return !e.expires.After(c.now()) }, maxCacheEntries)
		c.teamCache[key] = teamCacheEntry{member: true, expires: clock.Add(time.Minute)}
	}
	c.mu.Unlock()
	assert.LessOrEqual(t, len(c.teamCache), maxCacheEntries+1)
}

func TestPruneMap_DropsExpiredBeforeLive(t *testing.T) {
	clock := time.Unix(1000, 0)
	m := map[string]userCacheEntry{
		"dead-1": {expires: clock.Add(-time.Second)},
		"dead-2": {expires: clock.Add(-time.Minute)},
		"live-1": {expires: clock.Add(time.Hour)},
	}

	pruneMap(m, func(e userCacheEntry) bool { return !e.expires.After(clock) }, 2)

	assert.Len(t, m, 1)
	_, ok := m["live-1"]
	assert.True(t, ok, "expired entries go first; a live entry is evicted only when the cap still overflows")
}
