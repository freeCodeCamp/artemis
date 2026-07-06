package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSitePromote_LogsActionWithActor(t *testing.T) {
	cap := captureAccessLog(t)
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"team-a": true}},
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		store)

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote",
		nil,
		map[string]string{"Authorization": "Bearer good"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	m, ok := cap.findAction("site.promote", "success")
	require.True(t, ok, "site.promote success line emitted")
	assert.Equal(t, "alice", m["actor"])
	assert.Equal(t, "www", m["site"])
	assert.Equal(t, "20260420-141522-abc1234", m["deployId"])
	assert.Equal(t, "success", m["outcome"])
}
