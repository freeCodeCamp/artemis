package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/gc"
)

var fenceDeadline = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

func fencedHandlers(t *testing.T, store *fakeR2) (*Handlers, *fakeJWT, *fakeRegistry) {
	t.Helper()
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true, "staff": true}},
	}
	h, jwt := newTestHandlers(t, gh, standardSites(), store)
	reg := h.Registry.(*fakeRegistry)
	h.Audit = &fakeAudit{}
	return h, jwt, reg
}

func fenceErrorBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Error
}

func TestDeployFinalize_FencedReservedSiteRefusesTheAliasWrite(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, reg := fencedHandlers(t, store)
	reserveSite(h, reg, "www", fenceDeadline)

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	errObj := fenceErrorBody(t, w)
	assert.Equal(t, "site_reserved", errObj["code"])
	assert.Equal(t, fenceDeadline.Format(time.RFC3339), errObj["reservedUntil"])
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.aliases, "a reserved name must not return to the public internet")
}

func TestDeployFinalize_FencedDeletedSiteIsStillGone(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, reg := fencedHandlers(t, store)
	delete(reg.bySite, "www")

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, "site_gone", fenceErrorBody(t, w)["code"])
}

func TestSitePromote_FencedReservedSiteRefusesTheAliasWrite(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	reserveSite(h, reg, "www", fenceDeadline)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SitePromote)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "site_reserved", fenceErrorBody(t, w)["code"])
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.NotContains(t, store.aliases, "www/production",
		"the plain-bearer arm has no deploy-session JWT to revoke; only the row can fence it")
}

func TestSitePromote_FencedStaleSnapshotStillRefuses(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	reg.reserve("www", fenceDeadline)

	require.NotEmpty(t, h.Sites.Snapshot().TeamsForSite("www"),
		"the snapshot is deliberately left stale: its TTL fallback is 60s and it is read before the lock")

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SitePromote)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.NotContains(t, store.aliases, "www/production",
		"the fence must be the in-lock authoritative read, not the eventually-consistent snapshot")
}

func TestSitePromote_FencedDeletedSiteIsGone(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	delete(reg.bySite, "www")

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SitePromote)

	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, "site_gone", fenceErrorBody(t, w)["code"])
}

func rollbackBody(t *testing.T, to string) []byte {
	t.Helper()
	b, err := json.Marshal(SiteRollbackRequest{To: to})
	require.NoError(t, err)
	return b
}

func TestSiteRollback_FencedReservedSiteRefusesTheAliasWrite(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	reserveSite(h, reg, "www", fenceDeadline)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", rollbackBody(t, deployID),
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteRollback)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "site_reserved", fenceErrorBody(t, w)["code"])
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.NotContains(t, store.aliases, "www/production")
}

func TestSiteRollback_FencedStaleSnapshotStillRefuses(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	reg.reserve("www", fenceDeadline)

	require.NotEmpty(t, h.Sites.Snapshot().TeamsForSite("www"))

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", rollbackBody(t, deployID),
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteRollback)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.NotContains(t, store.aliases, "www/production")
}

func TestSiteDeployRestore_FencedReservedSiteRefusesTheMove(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["_trash/www/"+deployID+"/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	h.Trash = &fakeTrash{tombstonesBySite: map[string][]gc.Tombstone{
		"www": {{Site: "www", ID: deployID, TrashedAt: time.Now().UTC(), Bytes: 2}},
	}}
	h.TrashPrefixBase = "_trash/"
	h.TrashRecovery = 7 * 24 * time.Hour
	reserveSite(h, reg, "www", fenceDeadline)

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/deploys/{deployId}/restore",
		"/api/site/www/deploys/"+deployID+"/restore", nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteDeployRestore)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "site_reserved", fenceErrorBody(t, w)["code"])
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Empty(t, store.movePrefixSrcs,
		"restore would resurrect bytes at the origin prefix of a name being reclaimed")
}

func TestSiteUpdate_FencedReservedSiteRefusesTheTeamsChange(t *testing.T) {
	store := newFakeR2()
	h, _, reg := fencedHandlers(t, store)
	reserveSite(h, reg, "www", fenceDeadline)
	body, err := json.Marshal(SiteUpdateRequest{Teams: []string{"mallory-team"}})
	require.NoError(t, err)

	w := withSiteRoute(http.MethodPatch, "/api/site/{slug}",
		"/api/site/www", body,
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteUpdate)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "site_reserved", fenceErrorBody(t, w)["code"])
	assert.Equal(t, []string{"team-eng", "team-platform"}, reg.bySite["www"].Teams,
		"a reserved name's owners must not change while it waits to be reclaimed")
}

func TestSiteUpdate_FencedAbsentSiteKeepsIts404(t *testing.T) {
	store := newFakeR2()
	h, _, reg := fencedHandlers(t, store)
	delete(reg.bySite, "www")
	body, err := json.Marshal(SiteUpdateRequest{Teams: []string{"team-eng"}})
	require.NoError(t, err)

	w := withSiteRoute(http.MethodPatch, "/api/site/{slug}",
		"/api/site/www", body,
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteUpdate)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Equal(t, "not_found", fenceErrorBody(t, w)["code"],
		"PATCH's documented contract is 404 on an absent row; the fence must not silently make it 410")
}

func TestDeployFinalize_FencedMarkerWriteHappensUnderTheSiteLock(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	log := &eventLog{}
	logging := &loggingR2{fakeR2: store, log: log}
	h, jwt, _ := fencedHandlers(t, store)
	h.R2 = logging
	h.Locker = &fakeLocker{log: log}

	w := callFinalize(t, h, jwt, deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www", "put:www/deploys/"+deployID+"/"+gc.MarkerObjectName)
}

func TestDeployFinalize_FencedByteCountIsReadUnderTheSiteLock(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	log := &eventLog{}
	logging := &loggingR2{fakeR2: store, log: log}
	h, jwt, _ := fencedHandlers(t, store)
	h.R2 = logging
	h.Locker = &fakeLocker{log: log}

	w := callFinalize(t, h, jwt, deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www", "bytes:www/deploys/"+deployID+"/")
}

func TestSiteDeployRestore_FencedAbsentSiteIsStillGone(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	h, _, reg := fencedHandlers(t, store)
	h.Trash = &fakeTrash{}
	h.TrashPrefixBase = "_trash/"
	delete(reg.bySite, "www")

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/deploys/{deployId}/restore",
		"/api/site/www/deploys/"+deployID+"/restore", nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteDeployRestore)

	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Equal(t, "site_gone", fenceErrorBody(t, w)["code"])
}
