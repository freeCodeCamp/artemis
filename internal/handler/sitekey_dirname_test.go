package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prodShapedFormat = "<site>.freecode.camp/deploys/<ts>-<sha>/"

func TestEmitSiteChanged_CanonicalDirname(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	ob := &fakeOutbox{}
	h.Outbox = ob

	h.emitSiteChanged(context.Background(), "www")

	assert.Equal(t, []string{"www.freecode.camp"}, ob.sites,
		"site.changed payload must carry the R2 dirname (GC index key), not the registry slug")
}

func TestSitePurge_DirnameKeyedBytesAndTombstone(t *testing.T) {
	store := newFakeR2()
	store.objects["example.freecode.camp/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	require.NotEmpty(t, store.objects)
	for k := range store.objects {
		assert.Truef(t, hasPrefix(k, "_trash/example.freecode.camp/"),
			"every site object cascades into the dirname-keyed trash prefix, found %q", k)
	}
	assert.Equal(t, []string{"example.freecode.camp/"}, tomb.recorded,
		"site-level tombstone keyed by dirname so tombstone-purge deletes the real trash prefix")
}

func TestSiteDeployDelete_DirnameKeyedTombstone(t *testing.T) {
	deployID := "20260101-000000-old0001"
	store := newFakeR2()
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("old")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	defer store.mu.Unlock()
	_, live := store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"]
	assert.False(t, live, "deploy bytes must leave the live prefix")
	_, trashed := store.objects["_trash/www.freecode.camp/"+deployID+"/index.html"]
	assert.True(t, trashed, "deploy bytes must land under the dirname-keyed trash prefix")
	assert.Equal(t, []string{"www.freecode.camp/" + deployID}, tomb.recorded)
}
