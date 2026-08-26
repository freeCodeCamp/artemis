package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
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

func (o *orderTombstones) RecordSitePurge(ctx context.Context, site sitekey.Dirname) error {
	o.log.ops = append(o.log.ops, "row")
	return o.fakeTombstones.RecordSitePurge(ctx, site)
}

func (o *orderTombstones) RecordTombstone(ctx context.Context, site sitekey.Dirname, id string, bytes int64) error {
	o.log.ops = append(o.log.ops, "row")
	return o.fakeTombstones.RecordTombstone(ctx, site, id, bytes)
}

func registerExample(t *testing.T, h *Handlers) {
	t.Helper()
	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)
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

func TestDeployFinalize_RejectsWrongDeployID(t *testing.T) {
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), newFakeR2())

	tok, _, err := jwt.Sign("alice", "www", "20260420-141522-abc1234")
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})
	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/wrong-deploy/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)

	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "jwt_wrong_deploy",
		"the deployId comparison is the only live check in the scope guard; the site is pinned by "+
			"rendering the write target from claims.Site, and the subject by the signature itself")
}
