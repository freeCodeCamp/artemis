package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestGitHubClient_AuthorizeForSite_NoTeams(t *testing.T) {
	gh := newFakeGH()
	defer gh.Close()

	c := NewGitHubClient(GitHubClientConfig{APIBase: gh.server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute})

	ok, err := c.AuthorizeForSite(context.Background(), "ghp_test", "alice", nil)
	require.NoError(t, err)
	assert.False(t, ok)
}
