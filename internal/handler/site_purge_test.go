package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flakyMoveR2 struct {
	*fakeR2
	failMovesRemaining int
}

func (f *flakyMoveR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if f.failMovesRemaining > 0 {
		f.failMovesRemaining--
		return 0, errors.New("r2 move outage")
	}
	return f.fakeR2.MovePrefix(ctx, src, dst)
}

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

func TestSitePurge_SurvivesRequestCancellation(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		ctx,
	)
	require.Equal(t, http.StatusOK, w.Code,
		"whole-site purge MovePrefix runs on a ctx detached from the request deadline; a cancelled request must not leave a half-trashed tree + surviving registry row (TMO-1)")

	store.mu.Lock()
	defer store.mu.Unlock()
	for k := range store.objects {
		assert.Truef(t, hasPrefix(k, "_trash/example/"), "object cascaded to _trash despite cancelled request ctx, found live %q", k)
	}
	assert.Equal(t, []string{"example/"}, tomb.recorded, "site tombstone recorded, not skipped")
}

func TestSitePurge_FailedMoveKeepsSiteRetryable(t *testing.T) {
	store := &flakyMoveR2{fakeR2: newFakeR2(), failMovesRemaining: 1}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	failW := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
	require.Equal(t, http.StatusBadGateway, failW.Code, failW.Body.String())
	assert.Contains(t, failW.Body.String(), "r2_move_failed")

	listW := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, listW.Code)
	var rows []SiteRow
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &rows))
	slugs := make([]string, len(rows))
	for i, r := range rows {
		slugs[i] = r.Slug
	}
	assert.Contains(t, slugs, "example", "failed purge must not deregister the site (still retryable)")
	assert.Empty(t, tomb.recorded, "no tombstone written when the move failed")

	retryW := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
	require.Equal(t, http.StatusOK, retryW.Code, retryW.Body.String())
	assert.Contains(t, retryW.Body.String(), "purged")

	store.mu.Lock()
	for k := range store.objects {
		assert.Truef(t, hasPrefix(k, "_trash/example/"), "retry cascaded every example/ object into _trash, found %q live", k)
	}
	store.mu.Unlock()
	assert.Equal(t, []string{"example/"}, tomb.recorded, "retry records the site-level tombstone")

	gone := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, gone.Code)
	var after []SiteRow
	require.NoError(t, json.Unmarshal(gone.Body.Bytes(), &after))
	for _, r := range after {
		assert.NotEqual(t, "example", r.Slug, "successful purge deregisters the site")
	}
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
