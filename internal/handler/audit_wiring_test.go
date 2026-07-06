package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSitePromote_RecordsExactlyOneAudit(t *testing.T) {
	fa := &fakeAudit{}
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"team-a": true}},
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		store)
	h.Audit = fa

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		map[string]string{"Authorization": "Bearer good"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, fa.events, 1, "exactly one audit row per destructive action (V4)")
	assert.Equal(t, "site.promote", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "www", fa.events[0].Site)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteRegister_RecordsAuditWithCreatedBy(t *testing.T) {
	fa := &fakeAudit{}
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"staff": true}},
		},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())
	h.Audit = fa

	w := withChiRoute(http.MethodPost, "/api/site/register",
		"/api/site/register", []byte(`{"slug":"newsite","teams":["team-a"]}`),
		map[string]string{"Authorization": "Bearer good", "Content-Type": "application/json"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteRegister))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Len(t, fa.events, 1, "SiteRegister is audited (created_by)")
	assert.Equal(t, "site.register", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "newsite", fa.events[0].Site)
	assert.Equal(t, "alice", fa.events[0].Detail["createdBy"])
}

func TestSiteDeployDelete_RecordsExactlyOneAudit(t *testing.T) {
	fa := &fakeAudit{}
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	h.Audit = fa

	w := withChiRoute(http.MethodDelete, "/api/site/{site}/deploys/{deployId}",
		"/api/site/www/deploys/"+deployID, nil,
		map[string]string{"Authorization": "Bearer tok"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteDeployDelete))).ServeHTTP,
		context.Background())

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.deploy.delete", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, deployID, fa.events[0].DeployID)
	assert.Equal(t, "success", fa.events[0].Outcome)
}
