package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAliasGet_PreviewOK(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260513-101010-abcdef1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/www/alias/preview", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.AliasGet,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "www", body["site"])
	assert.Equal(t, "preview", body["mode"])
	assert.Equal(t, "20260513-101010-abcdef1", body["deployId"])
	assert.NotEmpty(t, body["url"])
}

func TestAliasGet_ProductionOK(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/production"] = "20260513-101010-abcdef1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/www/alias/production", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.AliasGet,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "production", body["mode"])
	assert.Equal(t, "20260513-101010-abcdef1", body["deployId"])
}

func TestAliasGet_BadMode(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/www/alias/banana", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.AliasGet,
	)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	err, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bad_request", err["code"])
}

func TestAliasGet_NoAlias(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/www/alias/preview", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.AliasGet,
	)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	err, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "no_alias", err["code"])
}

func TestAliasGet_RejectsUnauthorizedUser(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260513-101010-abcdef1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/www/alias/preview", nil,
		contextWithLogin(context.Background(), "carol", "tok"),
		h.AliasGet,
	)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestAliasGet_UnregisteredSite(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/alias/{mode}",
		"/api/site/never-registered/alias/preview", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.AliasGet,
	)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
