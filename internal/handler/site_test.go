package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withSiteRoute mirrors withChiRoute but routes "/api/site/{site}/<verb>"
// patterns and assumes RequireGitHubBearer-style context already on the
// request.
func withSiteRoute(method, pattern, target string, body []byte, ctx context.Context, fn http.HandlerFunc) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(method, pattern, fn)

	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSitePromote_Atomic(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"

	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	alias := store.aliases["www/production"]
	store.mu.Unlock()
	assert.Equal(t, "20260420-141522-abc1234", alias)
}

func TestSitePromote_NoPreviewAlias(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestSitePromote_RejectsUnauthorizedUser(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "d1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "carol", "tok"),
		h.SitePromote,
	)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestSiteRollback_ValidatesTargetExists(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/old-deploy/index.html"] = []byte("ok")
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "old-deploy"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	alias := store.aliases["www/production"]
	store.mu.Unlock()
	assert.Equal(t, "old-deploy", alias)
}

func TestSiteRollback_RejectsMissingTarget(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "swept-deploy"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

// TestSiteRollback_UsesHasPrefix — B6: existence probe must go through
// HasPrefix (one MaxKeys=1 list), not ListPrefix (full pagination).
func TestSiteRollback_UsesHasPrefix(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/old-deploy/index.html"] = []byte("a")
	store.objects["www/deploys/old-deploy/page.html"] = []byte("b")
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "old-deploy"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, 1, store.hasPrefixCalls, "rollback should probe via HasPrefix")
	assert.Equal(t, 0, store.listPrefixCalls, "rollback should not paginate ListPrefix")
}

func TestSiteRollback_BadJSON(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", []byte("not-json"),
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSiteRollback_MissingTo(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteRollbackRequest{To: ""})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSitePromote_UnregisteredSite(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/ghost/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestSiteDeploys_EmptyForUntouchedSite(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/deploys",
		"/api/site/learn/deploys", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeploys,
	)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, "[]", w.Body.String())
}

func TestSiteDeploys_GroupsByDeployID(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("a")
	store.objects["www/deploys/d1/assets/app.js"] = []byte("b")
	store.objects["www/deploys/d2/index.html"] = []byte("c")
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/deploys",
		"/api/site/www/deploys", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeploys,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

	ids := []string{}
	for _, d := range got {
		ids = append(ids, d["deployId"])
	}
	assert.ElementsMatch(t, []string{"d1", "d2"}, ids)
}
