package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGH is a minimal stub of the GitHub REST API used by the client.
//
//   - GET /user                      → returns {login}
//   - GET /orgs/<org>/teams/<slug>/memberships/<user> → 200 active / 404
type fakeGH struct {
	mux         *http.ServeMux
	server      *httptest.Server
	userCalls   atomic.Int32
	memberCalls atomic.Int32

	// behaviour switches
	userStatus    int
	userBody      string
	memberStatus  int
	memberBody    string
	rateLimitOnce atomic.Bool
	transient5xx  atomic.Bool
}

func newFakeGH() *fakeGH {
	f := &fakeGH{
		mux:          http.NewServeMux(),
		userStatus:   200,
		userBody:     `{"login":"alice"}`,
		memberStatus: 200,
		memberBody:   `{"state":"active"}`,
	}
	f.mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		f.userCalls.Add(1)
		if f.transient5xx.Load() {
			http.Error(w, `{"message":"oops"}`, http.StatusBadGateway)
			return
		}
		if f.rateLimitOnce.CompareAndSwap(true, false) {
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.userStatus)
		_, _ = w.Write([]byte(f.userBody))
	})
	f.mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
		f.memberCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.memberStatus)
		_, _ = w.Write([]byte(f.memberBody))
	})
	f.server = httptest.NewServer(f.mux)
	return f
}

func (f *fakeGH) Close() { f.server.Close() }

func TestGitHubClient_ValidateToken_OK(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  gh.server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	login, err := c.ValidateToken(context.Background(), "ghp_test")
	require.NoError(t, err)
	assert.Equal(t, "alice", login)
}

func TestGitHubClient_ValidateToken_RateLimit(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.rateLimitOnce.Store(true)

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "x", CacheTTL: time.Minute})

	_, err := c.ValidateToken(context.Background(), "ghp_test")
	require.Error(t, err)
	assert.True(t, IsGitHubRateLimited(err))
}

func TestGitHubClient_ValidateToken_5xx(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.transient5xx.Store(true)

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "x", CacheTTL: time.Minute})
	_, err := c.ValidateToken(context.Background(), "ghp_test")
	require.Error(t, err)
	assert.True(t, IsGitHubUnavailable(err))
}

func TestGitHubClient_Cache_HitsAndExpires(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "x", CacheTTL: 200 * time.Millisecond})

	for i := 0; i < 5; i++ {
		_, err := c.ValidateToken(context.Background(), "ghp_same")
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, gh.userCalls.Load(), "expected first call to populate cache")

	time.Sleep(250 * time.Millisecond)
	_, err := c.ValidateToken(context.Background(), "ghp_same")
	require.NoError(t, err)
	assert.EqualValues(t, 2, gh.userCalls.Load(), "expected cache to expire and refresh")
}

func TestGitHubClient_TeamMembership_Active(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.IsTeamMember(context.Background(), "ghp_test", "alice", "team-eng")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestGitHubClient_TeamMembership_NotMember(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.memberStatus = 404
	gh.memberBody = `{"message":"Not Found"}`

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.IsTeamMember(context.Background(), "ghp_test", "alice", "team-eng")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestGitHubClient_TeamMembership_Cached(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	for i := 0; i < 3; i++ {
		ok, err := c.IsTeamMember(context.Background(), "ghp_test", "alice", "team-eng")
		require.NoError(t, err)
		assert.True(t, ok)
	}
	assert.EqualValues(t, 1, gh.memberCalls.Load())
}

func TestGitHubClient_AuthorizeForSite_AnyTeamGrants(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	// First team probe returns 404, second 200.
	calls := atomic.Int32{}
	gh.mux = http.NewServeMux()
	gh.mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	})
	gh.mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"state":"active"}`))
	})
	gh.server.Close()
	gh.server = httptest.NewServer(gh.mux)

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.AuthorizeForSite(context.Background(), "ghp_test", "alice", []string{"team-other", "team-eng"})
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestCacheKey_NotRawToken — B3: raw bearer tokens must never appear as
// map keys. Heap dumps, debuggers, or future code paths that range over
// the cache must not leak credential material.
//
// Invariant: the userCache key is the sha256 of the raw token, truncated
// to 16 bytes and hex-encoded (32 hex chars).
func TestCacheKey_NotRawToken(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  gh.server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	raw := "ghp_super_secret_xxxxxxxxxxxxxxxxxxxx"
	_, err := c.ValidateToken(context.Background(), raw)
	require.NoError(t, err)

	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.userCache, 1, "ValidateToken should cache one entry")

	hexRe := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for k := range c.userCache {
		assert.NotEqual(t, raw, k, "cache key must not be raw bearer token")
		assert.NotContains(t, k, "ghp_", "cache key must not contain raw-token prefix")
		assert.True(t, hexRe.MatchString(k),
			"cache key must be 32-char hex (sha256 truncated 16 bytes); got %q", k)
	}
}

// TestValidateToken_NegativeCacheAbsorbsRetry — B1: repeated requests
// with the same invalid token must NOT hammer GitHub. After the first
// 401, subsequent calls within negative-TTL must short-circuit with the
// cached error.
func TestValidateToken_NegativeCacheAbsorbsRetry(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.userStatus = 401
	gh.userBody = `{"message":"Bad credentials"}`

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  gh.server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	for i := 0; i < 5; i++ {
		_, err := c.ValidateToken(context.Background(), "ghp_revoked")
		require.Error(t, err)
		assert.True(t, IsGitHubUnauthenticated(err), "expected 401 mapped to unauthenticated")
	}
	assert.EqualValues(t, 1, gh.userCalls.Load(),
		"negative result must be cached after first 401, not refetched")
}

// TestValidateToken_NegativeCache_NotForRateLimit — rate-limit/5xx are
// transient; caching them would extend the outage. Only steady 401/403
// (non-rate-limit) and 404 are cacheable negatives.
func TestValidateToken_NegativeCache_NotForRateLimit(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	calls := atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	gh.server.Close()
	gh.server = httptest.NewServer(mux)

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "x", CacheTTL: time.Minute})

	for i := 0; i < 3; i++ {
		_, err := c.ValidateToken(context.Background(), "ghp_x")
		require.Error(t, err)
		assert.True(t, IsGitHubRateLimited(err))
	}
	assert.EqualValues(t, 3, calls.Load(),
		"rate-limit must NOT be cached (transient); 5xx same rule")
}

// TestValidateToken_NegativeCache_NotFor5xx — same rule for upstream
// 5xx; caching a 503 freezes recovery for the negative-TTL window.
func TestValidateToken_NegativeCache_NotFor5xx(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.transient5xx.Store(true)

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "x", CacheTTL: time.Minute})

	for i := 0; i < 3; i++ {
		_, err := c.ValidateToken(context.Background(), "ghp_x")
		require.Error(t, err)
		assert.True(t, IsGitHubUnavailable(err))
	}
	assert.EqualValues(t, 3, gh.userCalls.Load(), "5xx must NOT be cached")
}

// TestValidateToken_SingleflightOnConcurrentMiss — B2: N concurrent
// cold-cache calls for the same token must coalesce into ONE upstream
// /user request. Without singleflight, all N race past the lock-check
// and fan out.
func TestValidateToken_SingleflightOnConcurrentMiss(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	// Slow the response so all goroutines pile up on the miss path.
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		gh.userCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	})
	gh.server.Close()
	gh.server = httptest.NewServer(mux)

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  gh.server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	const N = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			login, err := c.ValidateToken(context.Background(), "ghp_concurrent")
			assert.NoError(t, err)
			assert.Equal(t, "alice", login)
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, gh.userCalls.Load(),
		"expected singleflight to coalesce N concurrent misses into 1 upstream call")
}

func TestGitHubClient_AuthorizeForSite_NoTeams(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.AuthorizeForSite(context.Background(), "ghp_test", "alice", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}
