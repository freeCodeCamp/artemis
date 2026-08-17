package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secondaryLimitServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateToken_SecondaryRateLimitIsNotAnAuthFailure(t *testing.T) {
	var calls atomic.Int32
	srv := secondaryLimitServer(t, &calls)
	c := NewGitHubClient(GitHubClientConfig{APIBase: srv.URL})

	_, err := c.ValidateToken(context.Background(), "tok-abc")

	require.Error(t, err)
	assert.True(t, IsGitHubRateLimited(err),
		"a secondary-limit 403 carries Retry-After with non-zero X-RateLimit-Remaining; classifying it as "+
			"unauthenticated tells the operator a working credential is bad")
	assert.False(t, IsGitHubUnauthenticated(err))
}

func TestValidateToken_SecondaryRateLimitIsNeverNegativeCached(t *testing.T) {
	var calls atomic.Int32
	srv := secondaryLimitServer(t, &calls)
	c := NewGitHubClient(GitHubClientConfig{APIBase: srv.URL})

	_, _ = c.ValidateToken(context.Background(), "tok-abc")
	_, err := c.ValidateToken(context.Background(), "tok-abc")

	require.Error(t, err)
	assert.EqualValues(t, 2, calls.Load(),
		"the old path cached the throttle as ErrGitHubUnauthenticated for up to 30s, so every retry inside "+
			"that window got a 401 from cache without any upstream call; a throttle must stay uncached")
	assert.True(t, IsGitHubRateLimited(err))
}

func TestIsTeamMember_SecondaryRateLimitSurfacesAsRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := secondaryLimitServer(t, &calls)
	c := NewGitHubClient(GitHubClientConfig{APIBase: srv.URL, Org: "freeCodeCamp"})

	_, err := c.IsTeamMember(context.Background(), "tok", "alice", "team-eng")

	require.Error(t, err)
	assert.True(t, IsGitHubRateLimited(err))
}
