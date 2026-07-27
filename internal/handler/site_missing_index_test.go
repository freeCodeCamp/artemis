package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func promoteGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
}

func TestSitePromote_MissingIndex_Rejects(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t, promoteGH(), standardSites(), store)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "missing_index")

	store.mu.Lock()
	_, hasProd := store.aliases["www/production"]
	store.mu.Unlock()
	assert.False(t, hasProd, "production alias must not be written when the promoted deploy lacks index.html")
}

func TestSitePromote_DirectWrite_MissingIndex_Rejects(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, promoteGH(), standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{DeployID: "20260513-101010-cas9999"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "missing_index")

	store.mu.Lock()
	_, hasProd := store.aliases["www/production"]
	store.mu.Unlock()
	assert.False(t, hasProd, "direct-write promote must not write prod when target lacks index.html")
}

func TestSiteRollback_MissingIndex_Rejects(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/20260420-141522-old/page.html"] = []byte("no root index")
	h, _ := newTestHandlers(t, promoteGH(), standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260420-141522-old"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "missing_index")

	store.mu.Lock()
	_, hasProd := store.aliases["www/production"]
	store.mu.Unlock()
	assert.False(t, hasProd, "rollback to a deploy that exists but has no root index.html must not write prod")
}
