package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTombstones struct {
	recorded []string
	err      error
}

func (f *fakeTombstones) RecordTombstone(_ context.Context, site, id string, _ int64) error {
	if f.err != nil {
		return f.err
	}
	f.recorded = append(f.recorded, site+"/"+id)
	return nil
}

func authedGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
}

func callDeployDelete(h *Handlers, site, deployID string) *httptest.ResponseRecorder {
	return withSiteRoute(http.MethodDelete, "/api/site/{site}/deploys/{deployId}",
		"/api/site/"+site+"/deploys/"+deployID, nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeployDelete,
	)
}

func TestDelete_SurvivesRequestCancellation(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	w := withSiteRoute(http.MethodDelete, "/api/site/{site}/deploys/{deployId}",
		"/api/site/www/deploys/"+deployID, nil, ctx, h.SiteDeployDelete)

	require.Equal(t, http.StatusOK, w.Code,
		"tombstone-move runs on a ctx detached from the request deadline; a cancelled request must not abandon it mid-move (TMO-2)")
	store.mu.Lock()
	_, inTrash := store.objects["_trash/www/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.True(t, inTrash, "deploy reaches _trash despite cancelled request ctx")
	assert.Equal(t, []string{"www/" + deployID}, tomb.recorded, "tombstone recorded, not skipped")
}

func TestDelete_Tombstone(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("hi")
	store.objects[prefix+"app.js"] = []byte("js")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	_, srcGone := store.objects[prefix+"index.html"]
	_, inTrash := store.objects["_trash/www/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.False(t, srcGone, "deploy bytes moved out of the live prefix")
	assert.True(t, inTrash, "bytes moved to _trash (tombstone, not hard delete) (V5)")
	assert.Equal(t, []string{"www/" + deployID}, tomb.recorded, "tombstone recorded in store")
}

func TestDelete_AliasedConflict(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	store.aliases["www/production"] = deployID

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	tomb := &fakeTombstones{}
	h.Tombstones = tomb

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "deploy_aliased")

	store.mu.Lock()
	_, stillLive := store.objects["www/deploys/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.True(t, stillLive, "an aliased deploy is never moved/deleted (V1)")
	assert.Empty(t, tomb.recorded, "no tombstone recorded for an aliased deploy")
}

func TestDelete_PreviewAliasedConflict(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	store.aliases["www/preview"] = deployID

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "preview")
}

func TestDelete_BadDeployID(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}

	w := callDeployDelete(h, "www", "not-a-valid-id")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestDelete_Unauthorized(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}

	w := callDeployDelete(h, "unregistered-site", "20260420-141522-abc1234")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
