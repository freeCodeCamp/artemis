package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWhoAmI_ReturnsLoginAndAuthorizedSites(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"good": "alice"},
		userTeams: map[string]map[string]bool{
			"alice": {"team-eng": true},
		},
	}
	st := &fakeSites{bySite: map[string][]string{
		"www":   {"team-eng", "team-platform"},
		"learn": {"team-eng"},
		"news":  {"team-content"},
	}}
	h, _ := newTestHandlers(t, gh, st, newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "good"))
	w := httptest.NewRecorder()
	h.WhoAmI(w, r)

	require.Equal(t, http.StatusOK, w.Code)

	var got struct {
		Login           string   `json:"login"`
		AuthorizedSites []string `json:"authorizedSites"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "alice", got.Login)
	assert.ElementsMatch(t, []string{"www", "learn"}, got.AuthorizedSites)
}

func TestWhoAmI_UpstreamErrorReturns503(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"good": "alice"},
		upstreamErr: assert.AnError,
	}
	st := &fakeSites{bySite: map[string][]string{"www": {"team-eng"}}}
	h, _ := newTestHandlers(t, gh, st, newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "good"))
	w := httptest.NewRecorder()
	h.WhoAmI(w, r)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestWhoAmI_SkipsSitesWithNoTeams(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"good": "alice"},
		userTeams: map[string]map[string]bool{
			"alice": {"team-eng": true},
		},
	}
	// Site with no teams should be skipped (cannot grant via empty team list).
	st := &fakeSites{bySite: map[string][]string{
		"www":   {"team-eng"},
		"empty": {},
	}}
	h, _ := newTestHandlers(t, gh, st, newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "good"))
	w := httptest.NewRecorder()
	h.WhoAmI(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		AuthorizedSites []string `json:"authorizedSites"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.ElementsMatch(t, []string{"www"}, got.AuthorizedSites)
}

func TestWhoAmI_NoAuthorizedSites(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"g": "bob"},
		userTeams:   map[string]map[string]bool{},
	}
	st := &fakeSites{bySite: map[string][]string{
		"www": {"team-eng"},
	}}
	h, _ := newTestHandlers(t, gh, st, newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil).
		WithContext(contextWithLogin(context.Background(), "bob", "g"))
	w := httptest.NewRecorder()
	h.WhoAmI(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var got struct {
		Login           string   `json:"login"`
		AuthorizedSites []string `json:"authorizedSites"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.AuthorizedSites)
}

// contextWithLogin returns a context with both login + token attached, as
// the auth middleware would.
func contextWithLogin(parent context.Context, login, token string) context.Context {
	ctx := context.WithValue(parent, ctxKeyLogin, login)
	ctx = context.WithValue(ctx, ctxKeyToken, token)
	return ctx
}
