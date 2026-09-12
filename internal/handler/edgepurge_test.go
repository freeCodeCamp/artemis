package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakeEdgePurge struct {
	err    error
	purged chan []string
}

func newFakeEdgePurge() *fakeEdgePurge {
	return &fakeEdgePurge{purged: make(chan []string, 8)}
}

func (f *fakeEdgePurge) PurgeHosts(_ context.Context, hosts []string) error {
	f.purged <- hosts
	return f.err
}

func (f *fakeEdgePurge) wait(t *testing.T) []string {
	t.Helper()
	select {
	case hosts := <-f.purged:
		return hosts
	case <-time.After(2 * time.Second):
		t.Fatal("no purge arrived")
		return nil
	}
}

func (f *fakeEdgePurge) none(t *testing.T) {
	t.Helper()
	select {
	case hosts := <-f.purged:
		t.Fatalf("unexpected purge of %v", hosts)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDeployFinalize_PurgesTheSiteHostAtTheEdge(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	require.Equal(t, http.StatusOK, callFinalizeMode(t, h, jwt, deployID, "production").Code)

	assert.Equal(t, []string{"www.freecode.camp"}, purge.wait(t))
}

func TestDeployFinalize_PurgesThePreviewHostForAPreviewDeploy(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	require.Equal(t, http.StatusOK, callFinalizeMode(t, h, jwt, deployID, "preview").Code)

	assert.Equal(t, []string{"www.preview.freecode.camp"}, purge.wait(t))
}

func TestDeployFinalize_DoesNotPurgeWhenTheAliasWriteFails(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	store.putAliasFail = map[string]error{"www/preview": errors.New("r2 down")}
	h, jwt, _ := newFinalizeHandlers(t, store)
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	require.Equal(t, http.StatusBadGateway, callFinalize(t, h, jwt, deployID).Code)

	purge.none(t)
}

func TestDeployFinalize_SucceedsAndWarnsWhenThePurgeFails(t *testing.T) {
	logs := captureAccessLog(t)
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	purge := newFakeEdgePurge()
	purge.err = errors.New("cloudflare 403")
	h.EdgePurge = purge

	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code,
		"the edge heals itself when s-maxage expires; a purge failure must not fail the deploy")
	purge.wait(t)

	require.Eventually(t, func() bool { return logs.countMessage("edge.purge.failed") == 1 },
		2*time.Second, 10*time.Millisecond)
}

func TestSitePromote_PurgesTheProductionHost(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"team-a": true}},
		},
		&fakeSites{bySite: map[sitekey.Slug][]string{"www": {"team-a"}}},
		store)
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		map[string]string{"Authorization": "Bearer good"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"www.freecode.camp"}, purge.wait(t))
}

func TestDeployFinalize_WaitsForTheOriginAliasCacheBeforeThePurge(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	purge := newFakeEdgePurge()
	h.EdgePurge = purge
	h.EdgePurgeDelay = 300 * time.Millisecond

	started := time.Now()
	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code)
	purge.wait(t)

	assert.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond,
		"caddy caches the alias for cache_ttl; a purge before that re-caches the old deploy")
}

func TestPurgeEdge_WarnsWhenTheHostFormatHasNoScheme(t *testing.T) {
	logs := captureAccessLog(t)
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.PublicProductionURLFmt = "<site>.freecode.camp"
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	h.purgeEdge("www", "production")

	purge.none(t)
	assert.Equal(t, 1, logs.countMessage("edge.purge.skipped"),
		"a schemeless PUBLIC_URL_*_FORMAT parses to an empty host; a silent skip hides a dead feature")
}

func TestSiteDelete_PurgesBothHosts(t *testing.T) {
	store := newFakeR2()
	store.aliases["example/production"] = "20260420-141522-abc1234"
	store.aliases["example/preview"] = "20260421-090000-def5678"
	h, _ := newTestHandlers(t, staffCallerGH(),
		&fakeSites{bySite: map[sitekey.Slug][]string{"example": {"team-eng"}}}, store)
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDelete))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{"example.freecode.camp", "example.preview.freecode.camp"}, purge.wait(t))
}

func TestSiteUndelete_PurgesOnlyTheHostsItRestored(t *testing.T) {
	store := newFakeR2()
	store.putAliasFail = map[string]error{"www/preview": errors.New("r2 down")}
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	rr := &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
		PrevPreview:    "20260421-090000-def5678",
	}}
	h.Registry = rr
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Equal(t, []string{"www.freecode.camp"}, purge.wait(t),
		"production went back to R2 before preview failed; the edge must drop the stale production copy")
}

func TestPurgeEdge_CoalescesWritesToTheSameSiteInsideTheDelay(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	purge := newFakeEdgePurge()
	h.EdgePurge = purge
	h.EdgePurgeDelay = 600 * time.Millisecond

	started := time.Now()
	h.purgeEdge("www", "preview")
	time.Sleep(300 * time.Millisecond)
	h.purgeEdge("www", "production")

	assert.ElementsMatch(t, []string{"www.preview.freecode.camp", "www.freecode.camp"}, purge.wait(t),
		"finalize then promote is one purge, not two; the Free plan refills 5 requests a minute")
	assert.GreaterOrEqual(t, time.Since(started), 900*time.Millisecond,
		"the purge waits the full delay after the last write, or the edge re-caches the older alias")
}

func TestPurgeEdge_FiresByTheHoldCapUnderConstantWrites(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	purge := newFakeEdgePurge()
	h.EdgePurge = purge
	h.EdgePurgeDelay = 100 * time.Millisecond

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(40 * time.Millisecond):
				h.purgeEdge("www", "production")
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })

	assert.Equal(t, []string{"www.freecode.camp"}, purge.wait(t),
		"a deploy loop faster than the delay must not starve the edge forever")
}

func TestSiteDelete_PurgesWhenTheAliasProbeWasUnreadable(t *testing.T) {
	store := newFakeR2()
	store.getAliasFail = map[string]error{"example/production": errors.New("r2 502")}
	h, _ := newTestHandlers(t, staffCallerGH(),
		&fakeSites{bySite: map[sitekey.Slug][]string{"example": {"team-eng"}}}, store)
	h.Registry.(*fakeRegistry).getErr = registry.ErrNotFound
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDelete))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, []string{"example.freecode.camp", "example.preview.freecode.camp"}, purge.wait(t),
		"an unreadable probe may hide a served site; the aliases are gone, so the edge copy must go too")
}

func TestPurgeEdge_DoesNotCoalesceAcrossSites(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	h.purgeEdge("www", "production")
	h.purgeEdge("docs", "production")

	assert.ElementsMatch(t, [][]string{{"www.freecode.camp"}, {"docs.freecode.camp"}},
		[][]string{purge.wait(t), purge.wait(t)})
}

func TestPurgeEdge_PurgesAgainAfterTheBatchFired(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	h.purgeEdge("www", "production")
	purge.wait(t)
	h.purgeEdge("www", "production")

	assert.Equal(t, []string{"www.freecode.camp"}, purge.wait(t))
}

func TestSiteDelete_SkipsThePurgeWhenNothingWasServed(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(),
		&fakeSites{bySite: map[sitekey.Slug][]string{"example": {"team-eng"}}}, newFakeR2())
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	purge := newFakeEdgePurge()
	h.EdgePurge = purge

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDelete))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	purge.none(t)
}
