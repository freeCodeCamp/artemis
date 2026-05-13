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

// ---------------------------------------------------------------
// #28 — POST /api/site/{site}/promote body schema (deployId + CAS).
// ---------------------------------------------------------------

// TestSitePromote_DirectWriteSkipsPreviewRead verifies that when the
// caller supplies {deployId}, the handler writes prod = deployId
// directly and never reads the preview alias key. This is the B3-shape
// race-free path.
func TestSitePromote_DirectWriteSkipsPreviewRead(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	// Pre-seed a preview alias that would be promoted under the
	// legacy path. It must remain untouched here.
	store.aliases["www/preview"] = "20260420-141522-pre1234"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{DeployID: "20260513-101010-cas9999"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	prod := store.aliases["www/production"]
	preview := store.aliases["www/preview"]
	keys := append([]string(nil), store.getAliasKeys...)
	store.mu.Unlock()

	assert.Equal(t, "20260513-101010-cas9999", prod, "prod must equal supplied deployId")
	assert.Equal(t, "20260420-141522-pre1234", preview, "preview must be untouched")
	assert.NotContains(t, keys, "www/preview", "direct-write must not read preview alias")
}

// TestSitePromote_RejectsBadDeployIDFormat — invalid deployId is 400
// and prod is unchanged.
func TestSitePromote_RejectsBadDeployIDFormat(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{DeployID: "not-a-deploy-id"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bad_request", errObj["code"])

	store.mu.Lock()
	_, exists := store.aliases["www/production"]
	store.mu.Unlock()
	assert.False(t, exists, "rejected promote must not mutate prod alias")
}

// TestSitePromote_CAS_HappyPath — expectedCurrent matches → 200 +
// alias swapped.
func TestSitePromote_CAS_HappyPath(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-newer1"
	store.aliases["www/production"] = "20260101-101010-older1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{ExpectedCurrent: "20260101-101010-older1"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "20260420-141522-newer1", store.aliases["www/production"])
}

// TestSitePromote_CAS_DriftReturns409 — expectedCurrent mismatches →
// 409 alias_drift, prod alias untouched, response carries the actual
// current value so callers can re-read + retry.
func TestSitePromote_CAS_DriftReturns409(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-newer1"
	store.aliases["www/production"] = "20260101-101010-current"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{ExpectedCurrent: "20260101-101010-stale99"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alias_drift", errObj["code"])
	assert.Equal(t, "20260101-101010-current", resp["current"])
	assert.Equal(t, "www", resp["site"])

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "20260101-101010-current", store.aliases["www/production"], "prod must be untouched on drift")
}

// TestSitePromote_CAS_AndDeployID_AtomicSwap — both fields supplied:
// CAS read fires, then direct-write — preview alias is never read.
func TestSitePromote_CAS_AndDeployID_AtomicSwap(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-pre1234"
	store.aliases["www/production"] = "20260101-101010-current"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{
		DeployID:        "20260513-101010-cas9999",
		ExpectedCurrent: "20260101-101010-current",
	})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "20260513-101010-cas9999", store.aliases["www/production"])
	assert.NotContains(t, store.getAliasKeys, "www/preview", "direct-write+CAS must not read preview")
	assert.Contains(t, store.getAliasKeys, "www/production", "CAS branch must read production")
}

// TestSitePromote_CAS_NoExistingProdRejectsExpectation — expectedCurrent
// against a site with no prod alias yet returns 409 (current = "").
// Lets callers assert "no prod yet" by passing ExpectedCurrent="" via
// the same code path (verified by absence: empty ExpectedCurrent skips
// the CAS branch entirely — covered by TestSitePromote_Atomic).
func TestSitePromote_CAS_NoExistingProdRejectsExpectation(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-newer1"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SitePromoteRequest{ExpectedCurrent: "20260101-101010-anything"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

// TestSitePromote_BadJSON — malformed body short-circuits to 400.
func TestSitePromote_BadJSON(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", []byte("not-json"),
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------
// #29 — POST /api/site/{site}/rollback expectedCurrent CAS guard.
// ---------------------------------------------------------------

// TestSiteRollback_CAS_HappyPath — prod = D1, POST {to: D2,
// expectedCurrent: D1} → 200, prod = D2.
func TestSiteRollback_CAS_HappyPath(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d2-old-deploy/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "d2-old-deploy", ExpectedCurrent: "d1-current"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "d2-old-deploy", store.aliases["www/production"])
	assert.Contains(t, store.getAliasKeys, "www/production", "CAS branch must read prod alias")
}

// TestSiteRollback_CAS_DriftReturns409 — prod = D1, POST {to: D2,
// expectedCurrent: D0} → 409, body has current: D1, prod unchanged.
func TestSiteRollback_CAS_DriftReturns409(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d2-old-deploy/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "d2-old-deploy", ExpectedCurrent: "d0-stale"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	errObj, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alias_drift", errObj["code"])
	assert.Equal(t, "d1-current", resp["current"])
	assert.Equal(t, "www", resp["site"])

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "d1-current", store.aliases["www/production"], "prod must be untouched on drift")
}

// TestSiteRollback_NoCAS_LegacyPath — POST without expectedCurrent
// keeps existing behavior: HasPrefix check, then PutAlias, never
// reads the prod alias.
func TestSiteRollback_NoCAS_LegacyPath(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d2-old-deploy/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "d2-old-deploy"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, "d2-old-deploy", store.aliases["www/production"])
	assert.NotContains(t, store.getAliasKeys, "www/production", "legacy path must not read prod alias")
}

// TestSiteRollback_CAS_NoExistingProdRejectsExpectation — rollback
// CAS against a site with no prod alias yet returns 409 (current = "").
// Parity with the SitePromote CAS contract.
func TestSiteRollback_CAS_NoExistingProdRejectsExpectation(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.objects["www/deploys/d2-old-deploy/index.html"] = []byte("ok")
	h, _ := newTestHandlers(t, gh, standardSites(), store)

	body, _ := json.Marshal(SiteRollbackRequest{To: "d2-old-deploy", ExpectedCurrent: "d-anything"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}
