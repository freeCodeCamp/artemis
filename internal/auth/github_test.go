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
			w.Header().Set("X-RateLimit-Remaining", "0")
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

func TestGitHubClient_TeamMembership_MalformedBodyIsTransient(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()
	gh.memberStatus = 200
	gh.memberBody = `{"state":` // truncated JSON

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	_, err := c.IsTeamMember(context.Background(), "ghp_test", "alice", "team-eng")
	require.Error(t, err, "a 200 with an unparseable body must surface as a transient error, not a silent non-member")

	_, _ = c.IsTeamMember(context.Background(), "ghp_test", "alice", "team-eng")
	assert.EqualValues(t, 2, gh.memberCalls.Load(), "a parse failure must not be cached as a membership denial")
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

// TestGitHubClient_TeamMembership_PathEscape pins the URL-escape
// belt-and-suspenders on the GH team-membership probe. teamSlug and
// user are validated upstream (handler.teamSlugRe + GitHub /user) but
// any future caller that bypasses that validation must not be able
// to inject path segments through the GET URL.
func TestGitHubClient_TeamMembership_PathEscape(t *testing.T) {
	var capturedPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase: server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute,
	})

	// Crafted slug + user containing path-injection metacharacters.
	const slug = "../bypass/foo"
	const user = "a/b#frag"
	_, err := c.IsTeamMember(context.Background(), "ghp_test", user, slug)
	require.NoError(t, err)

	// The captured server-side path MUST treat the slug + user as
	// single path segments — every embedded `/` and `#` is
	// percent-encoded so the URL never re-routes through path
	// injection. PathEscape leaves the literal `..` alone (it is a
	// valid path-component byte sequence on the wire) but the lack
	// of an unescaped `/` between the dots and the rest means the
	// HTTP path parser sees a single opaque segment.
	assert.Contains(t, capturedPath, "..%2Fbypass%2Ffoo",
		"teamSlug not PathEscaped: got %q", capturedPath)
	assert.Contains(t, capturedPath, "a%2Fb%23frag",
		"user not PathEscaped: got %q", capturedPath)
	assert.NotContains(t, capturedPath, "/bypass/",
		"raw traversal slash leaked through: %q", capturedPath)
	assert.NotContains(t, capturedPath, "#frag",
		"unescaped fragment marker leaked through: %q", capturedPath)
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
		w.Header().Set("X-RateLimit-Remaining", "0")
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

// TestUserTeams_PaginatesAndFiltersByOrg — B9: returns slugs across
// pages, scoped to cfg.Org. Out-of-org memberships must be dropped.
func TestUserTeams_PaginatesAndFiltersByOrg(t *testing.T) {
	mux := http.NewServeMux()
	calls := atomic.Int32{}
	mux.HandleFunc("/user/teams", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			// 100 entries → forces another page request.
			out := `[`
			for i := 0; i < 100; i++ {
				if i > 0 {
					out += ","
				}
				out += `{"slug":"team-eng","organization":{"login":"freeCodeCamp"}}`
			}
			out += `]`
			_, _ = w.Write([]byte(out))
		case "2":
			_, _ = w.Write([]byte(`[
				{"slug":"team-content","organization":{"login":"freeCodeCamp"}},
				{"slug":"team-other-org","organization":{"login":"someOtherOrg"}}
			]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	teams, err := c.UserTeams(context.Background(), "ghp_x")
	require.NoError(t, err)
	// Page 1 yielded 100× team-eng (deduped is the caller's job; here we
	// just check filter + pagination), page 2 yielded team-content + one
	// out-of-org entry that must be dropped.
	assert.Contains(t, teams, "team-content")
	assert.NotContains(t, teams, "team-other-org",
		"out-of-org team must be filtered")
	assert.GreaterOrEqual(t, calls.Load(), int32(2),
		"must paginate past page 1")
}

// TestUserTeams_CachesByHashedToken — second call within TTL must NOT
// hit upstream. Inherits the hashToken cache key from B3.
func TestUserTeams_CachesByHashedToken(t *testing.T) {
	mux := http.NewServeMux()
	calls := atomic.Int32{}
	mux.HandleFunc("/user/teams", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`[{"slug":"team-eng","organization":{"login":"freeCodeCamp"}}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:  server.URL,
		Org:      "freeCodeCamp",
		CacheTTL: time.Minute,
	})

	for i := 0; i < 3; i++ {
		_, err := c.UserTeams(context.Background(), "ghp_same")
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, calls.Load(),
		"second/third UserTeams call must hit cache")
}

// TestValidateToken_RateLimitedByHeader — B16: rate-limit detection
// must use `X-RateLimit-Remaining: 0` header (GitHub's authoritative
// signal) rather than substring-matching the response body. Server
// here returns 403 with the header set but a body that does NOT
// contain "rate limit" — pre-B16 detection would mis-classify as
// plain unauthenticated.
func TestValidateToken_RateLimitedByHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1234567890")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"some other reason"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: server.URL, Org: "x", CacheTTL: time.Minute})
	_, err := c.ValidateToken(context.Background(), "ghp_x")
	require.Error(t, err)
	assert.True(t, IsGitHubRateLimited(err),
		"403 with X-RateLimit-Remaining: 0 must surface as rate-limit, got %v", err)
}

// TestValidateToken_403WithoutRateLimitHeader — bare 403 without the
// rate-limit header must surface as plain unauthenticated, not
// rate-limit. Pre-B16 the body-grep would still classify as
// rate-limit if the body coincidentally contained the substring.
func TestValidateToken_403WithoutRateLimitHeader(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: server.URL, Org: "x", CacheTTL: time.Minute})
	_, err := c.ValidateToken(context.Background(), "ghp_x")
	require.Error(t, err)
	assert.True(t, IsGitHubUnauthenticated(err))
	assert.False(t, IsGitHubRateLimited(err))
}

func TestGitHubClient_AuthorizeForSite_NoTeams(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.AuthorizeForSite(context.Background(), "ghp_test", "alice", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}
