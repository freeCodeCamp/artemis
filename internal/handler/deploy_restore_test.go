package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callDeployRestore(h *Handlers, site, deployID string) *httptest.ResponseRecorder {
	return withSiteRoute(http.MethodPost, "/api/site/{site}/deploys/{deployId}/restore",
		"/api/site/"+site+"/deploys/"+deployID+"/restore", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeployRestore,
	)
}

func callTrashList(h *Handlers, site string) *httptest.ResponseRecorder {
	return withSiteRoute(http.MethodGet, "/api/site/{site}/trash",
		"/api/site/"+site+"/trash", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteTrashList,
	)
}

func TestRestore_HappyPath(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["_trash/www/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	trash := &fakeTrash{}
	h.Trash = trash

	w := callDeployRestore(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	_, stillTrashed := store.objects["_trash/www/"+deployID+"/index.html"]
	_, live := store.objects["www/deploys/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.False(t, stillTrashed, "bytes moved out of trash")
	assert.True(t, live, "bytes moved back to the live deploy prefix")
	assert.Equal(t, []string{"www/" + deployID}, trash.restored)
	assert.Equal(t, []int64{2}, trash.restoredBytes, "handler passes the real post-move R2 byte count, not a store-supplied value")
	assert.Contains(t, w.Body.String(), `"status":"restored"`)
	assert.Contains(t, w.Body.String(), `"bytes":2`)
}

func TestRestore_AlreadyPurged(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{restoreErr: registry.ErrNotFound}

	w := callDeployRestore(h, "www", deployID)
	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "already_purged")
}

func TestRestore_IdempotentAlreadyActive(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{restoreErr: registry.ErrNotFound}

	w := callDeployRestore(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"restored"`)
}

func TestRestore_SiteGone(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["_trash/www/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{}
	require.NoError(t, h.Registry.Delete(context.Background(), "www"))

	w := callDeployRestore(h, "www", deployID)
	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "site_gone")
}

func TestRestore_Unauthorized(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{}

	w := callDeployRestore(h, "unregistered-site", "20260420-141522-abc1234")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestRestore_BadDeployID(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{}

	w := callDeployRestore(h, "www", "not-a-valid-id")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestRestore_TrashNotConfigured(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)

	w := callDeployRestore(h, "www", "20260420-141522-abc1234")
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

func TestTrashList_ReturnsEntries(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	trashedAt := time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)
	h.Trash = &fakeTrash{tombstonesBySite: map[string][]gc.Tombstone{
		"www": {{Site: "www", ID: "d1", TrashedAt: trashedAt, Bytes: 100}},
	}}
	h.TrashRecovery = 7 * 24 * time.Hour

	w := callTrashList(h, "www")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "d1", got[0]["deployId"])
	assert.Equal(t, "2026-04-20T14:15:22Z", got[0]["trashedAt"])
	assert.Equal(t, "2026-04-27T14:15:22Z", got[0]["expiresAt"])
	assert.EqualValues(t, 100, got[0]["bytes"])
}

func TestTrashList_Unauthorized(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{}

	w := callTrashList(h, "unregistered-site")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

type fakeTombstoneTrash struct {
	now  func() time.Time
	rows map[string]gc.Tombstone
}

func newFakeTombstoneTrash(now func() time.Time) *fakeTombstoneTrash {
	return &fakeTombstoneTrash{now: now, rows: map[string]gc.Tombstone{}}
}

func (f *fakeTombstoneTrash) RecordTombstone(_ context.Context, site, id string, bytes int64) error {
	f.rows[site+"/"+id] = gc.Tombstone{Site: site, ID: id, TrashedAt: f.now(), Bytes: bytes}
	return nil
}

func (f *fakeTombstoneTrash) TombstonesForSite(_ context.Context, site string) ([]gc.Tombstone, error) {
	var out []gc.Tombstone
	for _, t := range f.rows {
		if t.Site == site {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTombstoneTrash) RestoreDeploy(_ context.Context, site, id string, _ time.Time, _ int64) error {
	key := site + "/" + id
	if _, ok := f.rows[key]; !ok {
		return registry.ErrNotFound
	}
	delete(f.rows, key)
	return nil
}

func TestRestore_EndToEnd_AfterRealDelete(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("<h1>hello world</h1>")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	fake := newFakeTombstoneTrash(time.Now)
	h.Tombstones = fake
	h.Trash = fake

	delW := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, delW.Code, delW.Body.String())

	store.mu.Lock()
	_, stillLive := store.objects["www/deploys/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.False(t, stillLive, "delete moved bytes out of the live prefix")

	restoreW := callDeployRestore(h, "www", deployID)
	require.Equal(t, http.StatusOK, restoreW.Code, restoreW.Body.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(restoreW.Body.Bytes(), &got))
	bytes, ok := got["bytes"].(float64)
	require.True(t, ok, "bytes field present and numeric")
	assert.Greater(t, bytes, float64(0), "restore reports real R2 bytes, not the tombstone's always-0 stored value")
}
