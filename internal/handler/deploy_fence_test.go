package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakeDeployFence struct {
	marked map[string]time.Duration
	err    error
}

func newFakeDeployFence() *fakeDeployFence {
	return &fakeDeployFence{marked: map[string]time.Duration{}}
}

func (f *fakeDeployFence) MarkDeployFinalized(_ context.Context, site sitekey.Slug, id string, ttl time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.marked[string(site)+"/"+id] = ttl
	return nil
}

func (f *fakeDeployFence) IsDeployFinalized(_ context.Context, site sitekey.Slug, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	_, ok := f.marked[string(site)+"/"+id]
	return ok, nil
}

func TestDeployUpload_RefusesAWriteIntoAFinalizedDeploy(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	fence := newFakeDeployFence()
	h.DeployFence = fence
	require.NoError(t, fence.MarkDeployFinalized(context.Background(), "www", deployID, time.Minute))

	w := callUpload(t, h, jwt, deployID, "index.html", []byte("evil"))

	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Empty(t, store.objects,
		"the production alias already points at this prefix, so the write would edit a live deploy in "+
			"place; tenet 5 says a deploy is immutable and there is no edit")
}

func TestDeployUpload_AllowsAWriteIntoAPendingDeploy(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.DeployFence = newFakeDeployFence()

	w := callUpload(t, h, jwt, deployID, "index.html", []byte("hi"))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotEmpty(t, store.objects, "the ordinary upload path must not be fenced")
}

func TestDeployUpload_RefusesWhenTheFenceCannotBeRead(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.DeployFence = &fakeDeployFence{err: errors.New("valkey unreachable")}

	w := callUpload(t, h, jwt, deployID, "index.html", []byte("hi"))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Empty(t, store.objects,
		"answering the upload on a cache fault reinstates the overwrite this fence prevents. Failing "+
			"closed costs nothing extra: readyz already returns 503 on a valkey ping error before it "+
			"reaches its degraded branch, so a replica whose valkey is down is already out of service")
}

func TestDeployFinalize_MarksTheDeployFencedForTheLifeOfThePermit(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	fence := newFakeDeployFence()
	h.DeployFence = fence
	h.DeployJWTTTL = 900 * time.Second

	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code)

	assert.Equal(t, 900*time.Second, fence.marked["www/"+deployID],
		"the fence exists only to outlive the permit that could abuse it, so it carries the jwt ttl")
}

func callUpload(t *testing.T, h *Handlers, jwt *fakeJWT, deployID, relPath string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	return withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path="+relPath, body,
		map[string]string{"Authorization": "Bearer " + tok},
		RequestID(h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload))).ServeHTTP,
		context.Background(),
	)
}

func TestDeployFinalize_SucceedsAndWarnsWithNoFenceWired(t *testing.T) {
	logs := captureAccessLog(t)
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	h.DeployFence = nil

	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code,
		"the fence is optional wiring; its absence must not fail a deploy")
	assert.Equal(t, 1, logs.countMessage("deploy.fence.unwired"),
		"a silent skip leaves the deploy overwritable for the life of the permit with nobody told")
}

func TestDeployFinalize_SucceedsAndReportsWhenTheFenceWriteFails(t *testing.T) {
	logs := captureAccessLog(t)
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	h.DeployFence = &fakeDeployFence{err: errors.New("valkey unreachable")}

	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code,
		"the marker, the alias and the index row are already committed; failing here would report a "+
			"rollback that did not happen")
	assert.Equal(t, 1, logs.countMessage("deploy.fence.failed"))
}
