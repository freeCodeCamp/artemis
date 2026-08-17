package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type orderLog struct{ ops []string }

type orderR2 struct {
	*fakeR2
	log *orderLog
}

func (o *orderR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	o.log.ops = append(o.log.ops, "bytes")
	return o.fakeR2.MovePrefix(ctx, src, dst)
}

type orderTombstones struct {
	fakeTombstones
	log *orderLog
}

func (o *orderTombstones) RecordSitePurge(ctx context.Context, site string) error {
	o.log.ops = append(o.log.ops, "row")
	return o.fakeTombstones.RecordSitePurge(ctx, site)
}

func (o *orderTombstones) RecordTombstone(ctx context.Context, site, id string, bytes int64) error {
	o.log.ops = append(o.log.ops, "row")
	return o.fakeTombstones.RecordTombstone(ctx, site, id, bytes)
}

func registerExample(t *testing.T, h *Handlers) {
	t.Helper()
	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)
}

func callPurge(h *Handlers) *httptest.ResponseRecorder {
	return withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
}

func TestSitePurge_RecordsTheSiteTombstoneBeforeMovingBytes(t *testing.T) {
	log := &orderLog{}
	store := &orderR2{fakeR2: newFakeR2(), log: log}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	tomb := &orderTombstones{log: log}
	h.Tombstones = tomb
	registerExample(t, h)

	w := callPurge(h)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.NotEmpty(t, log.ops)
	assert.Equal(t, "row", log.ops[0],
		"a crash between the two writes must leave a site tombstone naming bytes still in place — never a "+
			"whole site in _trash/ that no tombstone dates, which tombstone-purge, the index and reconcile "+
			"all fail to list, forever")
}

func TestSitePurge_LeavesBytesInPlaceWhenTheRowWriteFails(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{err: errors.New("pg down")}
	registerExample(t, h)

	w := callPurge(h)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "tombstone_record_failed")

	store.mu.Lock()
	defer store.mu.Unlock()
	_, live := store.objects["example/deploys/20260420-141522-abc1234/index.html"]
	assert.True(t, live,
		"bytes must not move once the row that would date them is known to have failed; the site stays "+
			"registered and the purge is retryable")
}

func TestSiteDeployDelete_RecordsTheTombstoneBeforeMovingBytes(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	log := &orderLog{}
	store := &orderR2{fakeR2: newFakeR2(), log: log}
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	tomb := &orderTombstones{log: log}
	h.Tombstones = tomb

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.NotEmpty(t, log.ops)
	assert.Equal(t, "row", log.ops[0],
		"same rule as gc-site and reconcile: the tombstone row lands first, so a crash strands bytes at the "+
			"deploy prefix where the drift sweep reports them, not in _trash/ where nothing lists them")
}

func TestSiteDeployDelete_LeavesBytesInPlaceWhenTheRowWriteFails(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{err: errors.New("pg down")}

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "tombstone_record_failed")

	store.mu.Lock()
	defer store.mu.Unlock()
	_, live := store.objects["www/deploys/"+deployID+"/index.html"]
	assert.True(t, live,
		"a deploy whose tombstone could not be written keeps serving from its prefix and the delete is "+
			"retryable; moving it first would orphan it in _trash/ with no row to date it")
}
