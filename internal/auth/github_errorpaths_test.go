package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingGetTeamCache struct {
	err error
}

func (f failingGetTeamCache) Get(context.Context, string) ([]string, bool, error) {
	return nil, false, f.err
}

func (failingGetTeamCache) Set(context.Context, string, []string) error { return nil }

func TestUserTeams_DurableGetError_SurfacesNotRefetches(t *testing.T) {
	getErr := errors.New("valkey down")

	teamsCalls := atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	})
	mux.HandleFunc("/user/teams", func(w http.ResponseWriter, r *http.Request) {
		teamsCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"slug":"staff","organization":{"login":"freeCodeCamp"}}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase:   server.URL,
		Org:       "freeCodeCamp",
		CacheTTL:  time.Minute,
		TeamCache: failingGetTeamCache{err: getErr},
	})

	teams, err := c.UserTeams(context.Background(), "ghp_x")
	require.Error(t, err, "a durable-cache Get error must fail auth, not silently re-fetch")
	require.Nil(t, teams)
	assert.ErrorIs(t, err, getErr,
		"the Get failure must propagate to the caller so auth fails closed")
	assert.EqualValues(t, 0, teamsCalls.Load(),
		"a durable Get error must NOT fall through to a fresh GitHub /user/teams fetch")
}

func TestFetchTeamMembership_StatusClassification(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(w http.ResponseWriter)
		assertErr   func(t *testing.T, err error)
		expectCache bool
	}{
		{
			name: "rate limit is transient",
			configure: func(w http.ResponseWriter) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(http.StatusForbidden)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubRateLimited(err),
					"403 + X-RateLimit-Remaining:0 must map to rate-limited, got %v", err)
			},
		},
		{
			name: "5xx is unavailable",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadGateway)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubUnavailable(err),
					"502 must map to upstream-unavailable, got %v", err)
			},
		},
		{
			name: "unexpected status is a generic error",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusTeapot)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.False(t, IsGitHubRateLimited(err))
				assert.False(t, IsGitHubUnavailable(err))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			memberCalls := atomic.Int32{}
			mux := http.NewServeMux()
			mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"login":"alice"}`))
			})
			mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
				memberCalls.Add(1)
				tt.configure(w)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			c := NewGitHubClient(GitHubClientConfig{
				APIBase: server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute,
			})

			_, err := c.IsTeamMember(context.Background(), "ghp_x", "alice", "team-eng")
			tt.assertErr(t, err)

			_, _ = c.IsTeamMember(context.Background(), "ghp_x", "alice", "team-eng")
			assert.EqualValues(t, 2, memberCalls.Load(),
				"a transient/unexpected membership status must NOT be cached; second call re-probes")
		})
	}
}

func TestFetchUserTeams_StatusClassification(t *testing.T) {
	tests := []struct {
		name      string
		configure func(w http.ResponseWriter)
		assertErr func(t *testing.T, err error)
	}{
		{
			name: "401 is unauthenticated",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubUnauthenticated(err), "got %v", err)
			},
		},
		{
			name: "plain 403 is unauthenticated",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusForbidden)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubUnauthenticated(err), "got %v", err)
				assert.False(t, IsGitHubRateLimited(err))
			},
		},
		{
			name: "403 + rate-limit header is rate-limited",
			configure: func(w http.ResponseWriter) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(http.StatusForbidden)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubRateLimited(err), "got %v", err)
				assert.False(t, IsGitHubUnauthenticated(err))
			},
		},
		{
			name: "5xx is unavailable",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadGateway)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.True(t, IsGitHubUnavailable(err), "got %v", err)
			},
		},
		{
			name: "unexpected status is a generic error",
			configure: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusTeapot)
			},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.Error(t, err)
				assert.False(t, IsGitHubRateLimited(err))
				assert.False(t, IsGitHubUnavailable(err))
				assert.False(t, IsGitHubUnauthenticated(err))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"login":"alice"}`))
			})
			mux.HandleFunc("/user/teams", func(w http.ResponseWriter, r *http.Request) {
				tt.configure(w)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			c := NewGitHubClient(GitHubClientConfig{
				APIBase: server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute,
			})

			_, err := c.UserTeams(context.Background(), "ghp_x")
			tt.assertErr(t, err)
		})
	}
}

func TestFetchUser_404CachesNegative(t *testing.T) {
	userCalls := atomic.Int32{}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		userCalls.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase: server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute,
	})

	_, err := c.ValidateToken(context.Background(), "ghp_x")
	require.Error(t, err)
	assert.True(t, IsGitHubUnauthenticated(err),
		"404 on /user must map to a cached unauthenticated negative, got %v", err)

	for i := 0; i < 3; i++ {
		_, err := c.ValidateToken(context.Background(), "ghp_x")
		require.Error(t, err)
		assert.True(t, IsGitHubUnauthenticated(err))
	}
	assert.EqualValues(t, 1, userCalls.Load(),
		"404 negative must be cached; repeat calls must not re-hit upstream")
}

func TestFetchUser_UnexpectedStatus_GenericError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"message":"teapot"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	c := NewGitHubClient(GitHubClientConfig{
		APIBase: server.URL, Org: "freeCodeCamp", CacheTTL: time.Minute,
	})

	_, err := c.ValidateToken(context.Background(), "ghp_x")
	require.Error(t, err)
	assert.False(t, IsGitHubUnauthenticated(err),
		"an unexpected /user status must not be classified as a cacheable auth negative")
	assert.False(t, IsGitHubRateLimited(err))
	assert.False(t, IsGitHubUnavailable(err))
}
