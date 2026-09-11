package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
