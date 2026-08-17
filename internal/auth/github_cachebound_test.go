package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"active"}`))
	}))
	t.Cleanup(srv.Close)
	tc := NewGitHubClient(GitHubClientConfig{APIBase: srv.URL, Org: "freeCodeCamp",
		Now: func() time.Time { return clock }})
	for i := 0; i < maxCacheEntries+64; i++ {
		_, err := tc.IsTeamMember(context.Background(), "tok", fmt.Sprintf("u-%d", i), "t")
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, len(tc.teamCache), maxCacheEntries,
		"driven through IsTeamMember so the assertion fails if fetchTeamMembership stops pruning; the "+
			"first version of this test called pruneMap from its own loop and measured its own bookkeeping")
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
