package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIndex struct {
	finalized      []string
	finalizedBytes int64
	aliased        []string
	fail           bool
}

func (f *fakeIndex) FinalizeAtomic(_ context.Context, site, deployID, mode string, _ time.Time, bytes int64) error {
	if f.fail {
		return errors.New("pg down")
	}
	f.finalized = append(f.finalized, site+"/"+deployID+"/"+mode)
	f.finalizedBytes = bytes
	return nil
}

func (f *fakeIndex) AliasAtomic(_ context.Context, site, name, deployID string, _ time.Time) error {
	if f.fail {
		return errors.New("pg down")
	}
	f.aliased = append(f.aliased, site+"/"+name+"/"+deployID)
	return nil
}

func TestDeployFinalize_PGWriteThrough(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	idx := &fakeIndex{}
	h.Index = idx
	ob := &fakeOutbox{}
	h.Outbox = ob

	deployID := "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("hi")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Equal(t, []string{"www.freecode.camp/" + deployID + "/preview"}, idx.finalized,
		"finalize must index deploy+alias+event transactionally under the dirname key")
	assert.Empty(t, ob.sites, "tx path owns the outbox event; no duplicate direct emit")

	var wantBytes int64
	for k, v := range store.objects {
		if strings.HasPrefix(k, "www.freecode.camp/deploys/"+deployID+"/") {
			wantBytes += int64(len(v))
		}
	}
	assert.Positive(t, wantBytes)
	assert.Equal(t, wantBytes, idx.finalizedBytes, "finalize must record the deploy's R2 byte size, not 0")
}

func TestDeployFinalize_PGWriteFailure502(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.Index = &fakeIndex{fail: true}

	deployID := "20260420-141522-abc1234"
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "pg_write_failed")
}

func TestSitePromote_PGWriteThrough(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	idx := &fakeIndex{}
	h.Index = idx
	ob := &fakeOutbox{}
	h.Outbox = ob

	deployID := "20260420-141522-abc1234"
	body, _ := json.Marshal(SitePromoteRequest{DeployID: deployID})

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Equal(t, []string{"www.freecode.camp/production/" + deployID}, idx.aliased,
		"promote must upsert the PG alias row so the GC planner sees the new pin")
	assert.Empty(t, ob.sites)
}

func TestSiteRollback_PGWriteThrough(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	idx := &fakeIndex{}
	h.Index = idx

	deployID := "20260101-000000-old0001"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("old")
	body, _ := json.Marshal(SiteRollbackRequest{To: deployID})

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Equal(t, []string{"www.freecode.camp/production/" + deployID}, idx.aliased)
}
