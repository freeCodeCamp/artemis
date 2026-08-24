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

type failAliasPutR2 struct {
	*fakeR2
}

func (f *failAliasPutR2) PutAlias(_ context.Context, _, _ string) error {
	return errors.New("r2 put outage")
}

func callFinalize(t *testing.T, h *Handlers, jwt *fakeJWT, deployID string) *httptest.ResponseRecorder {
	t.Helper()
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})
	return withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
}

func newFinalizeHandlers(t *testing.T, store R2Store) (*Handlers, *fakeJWT, *fakeAudit) {
	t.Helper()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	fa := &fakeAudit{}
	h.Audit = fa
	return h, jwt, fa
}

func TestDeployFinalize_RetriesTheIndexWriteInsideTheCommitWindow(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, fa := newFinalizeHandlers(t, store)
	idx := &fakeIndex{failTimes: 2}
	h.Index = idx

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, idx.finalized, 1,
		"the marker and the alias are already committed to r2; a transient pg fault on the last leg "+
			"must not strand a published deploy with no index row")
	assert.Equal(t, 3, idx.attempts)
	require.Len(t, fa.events, 1)
	assert.Equal(t, "deploy.finalize", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestDeployFinalize_StopsRetryingTheIndexWriteAtTheCap(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	idx := &fakeIndex{failTimes: 99}
	h.Index = idx

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "pg_write_failed")
	assert.Equal(t, indexCommitAttempts, idx.attempts,
		"the retry runs inside the per-site advisory lock, so its hold must be bounded")
}

func TestDeployFinalize_AuditsThePartialCommitWhenTheIndexWriteFails(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, fa := newFinalizeHandlers(t, store)
	h.Index = &fakeIndex{fail: true}

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	body := decodeSingleBody(t, w)
	assert.Equal(t, "pg_write_failed", body["error"].(map[string]any)["code"])
	require.Len(t, fa.events, 1,
		"the marker is written and the alias is live; audit_log is the only durable record of what "+
			"went public without an index row")
	assert.Equal(t, "deploy.finalize", fa.events[0].Action)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "index", fa.events[0].Detail["stage"])
	assert.Equal(t, "preview", fa.events[0].Detail["mode"])
}

func TestDeployFinalize_AuditsTheAliasFailure(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	base := newFakeR2()
	base.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	store := &failAliasPutR2{fakeR2: base}
	h, jwt, fa := newFinalizeHandlers(t, store)

	w := callFinalize(t, h, jwt, deployID)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "r2_put_failed")
	require.Len(t, fa.events, 1,
		"the marker is committed and unindexed even though nothing went live; reindex will adopt it")
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "alias", fa.events[0].Detail["stage"])
}

func TestSitePromote_RetriesTheAliasIndexWrite(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	store.aliases["www/preview"] = deployID
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	idx := &fakeIndex{failTimes: 1}
	h.Index = idx
	fa := &fakeAudit{}
	h.Audit = fa

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, idx.aliased, 1,
		"nothing reconciles r2 aliases against the aliases table, so a lost promote index write is "+
			"repaired by no cron at all")
	assert.Equal(t, 2, idx.attempts)
	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.promote", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteRollback_RetriesTheAliasIndexWrite(t *testing.T) {
	deployID := "20260101-000000-old0001"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("old")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	idx := &fakeIndex{failTimes: 1}
	h.Index = idx
	fa := &fakeAudit{}
	h.Audit = fa

	body, _ := json.Marshal(SiteRollbackRequest{To: deployID})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, idx.aliased, 1)
	assert.Equal(t, 2, idx.attempts)
	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.rollback", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}
