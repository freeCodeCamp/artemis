package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSitePurge(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["example/deploys/20260101-000000-old0001/index.html"] = []byte("old")
	store.aliases["example/production"] = "20260420-141522-abc1234"
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
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
	assert.Contains(t, w.Body.String(), "purged")

	store.mu.Lock()
	defer store.mu.Unlock()
	for k := range store.objects {
		assert.Truef(t, hasPrefix(k, "_trash/example/"), "every example/ object cascaded into _trash, found %q live", k)
	}
	assert.Equal(t, []string{"example/"}, tomb.recorded, "site-level tombstone recorded (empty id = whole-site purge)")
}

func TestSiteDelete_NoPurge_LeavesBytes(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	w := callDelete(h, "example", "alice", "tok")
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	store.mu.Lock()
	_, stillThere := store.objects["example/deploys/20260420-141522-abc1234/index.html"]
	store.mu.Unlock()
	assert.True(t, stillThere, "plain deregister (no purge) leaves R2 bytes untouched")
}
