package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bearerTok() map[string]string { return map[string]string{"Authorization": "Bearer tok"} }

func TestSiteDelete_RecordsExactlyOneAudit(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(),
		&fakeSites{bySite: map[string][]string{"example": {"team-eng"}}}, newFakeR2())
	fa := &fakeAudit{}
	h.Audit = fa

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDelete))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a site delete records exactly one audit row")
	assert.Equal(t, "site.delete", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "example", fa.events[0].Site)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSitePurge_RecordsExactlyOneAudit(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.aliases["example/production"] = "20260420-141522-abc1234"
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(),
		&fakeSites{bySite: map[string][]string{"example": {"team-eng"}}}, store)
	h.Tombstones = &fakeTombstones{}
	fa := &fakeAudit{}
	h.Audit = fa

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDelete))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a whole-site purge records exactly one audit row")
	assert.Equal(t, "site.purge", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "example", fa.events[0].Site)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteRollback_RecordsExactlyOneAudit(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/20260419-090000-d2/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	fa := &fakeAudit{}
	h.Audit = fa

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260419-090000-d2", ExpectedCurrent: "d1-current"})
	w := withChiRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteRollback))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a rollback records exactly one audit row")
	assert.Equal(t, "site.rollback", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "www", fa.events[0].Site)
	assert.Equal(t, "success", fa.events[0].Outcome)
	assert.Equal(t, "20260419-090000-d2", fa.events[0].Detail["to"], "the target deploy is captured in the audit detail")
}

func TestRestore_RecordsAuditWithIdempotentOutcome(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Trash = &fakeTrash{restoreErr: registry.ErrNotFound}
	fa := &fakeAudit{}
	h.Audit = fa

	w := withChiRoute(http.MethodPost, "/api/site/{site}/deploys/{deployId}/restore",
		"/api/site/www/deploys/"+deployID+"/restore", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDeployRestore))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "an idempotent restore still records exactly one audit row")
	assert.Equal(t, "site.deploy.restore", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, deployID, fa.events[0].DeployID)
	assert.Equal(t, "idempotent", fa.events[0].Outcome,
		"the non-success outcome is audited verbatim, not coerced to success")
}
