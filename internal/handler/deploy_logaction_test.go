package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployInit_LogsActionWithActor(t *testing.T) {
	cap := captureAccessLog(t)
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"team-a": true}},
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		newFakeR2())

	chain := RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.DeployInit)))
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", strings.NewReader(`{"site":"www","sha":"abcdef1234"}`))
	r.Header.Set("Authorization", "Bearer good")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	start, ok := cap.findAction("deploy.init", "start")
	require.True(t, ok, "deploy.init start line emitted")
	assert.Equal(t, "alice", start["actor"])
	assert.Equal(t, "www", start["site"])

	done, ok := cap.findAction("deploy.init", "success")
	require.True(t, ok, "deploy.init success line emitted")
	assert.Equal(t, "alice", done["actor"])
	assert.Equal(t, "www", done["site"])
	assert.NotEmpty(t, done["deployId"], "success line carries the minted deployId")
}

func TestDeployInit_LogsDeniedWithActor(t *testing.T) {
	cap := captureAccessLog(t)
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "mallory"},
			userTeams:   map[string]map[string]bool{"mallory": {"other": true}},
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		newFakeR2())

	chain := RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.DeployInit)))
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", strings.NewReader(`{"site":"www","sha":"abcdef1234"}`))
	r.Header.Set("Authorization", "Bearer good")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, r)

	require.Equal(t, http.StatusForbidden, w.Code)
	denied, ok := cap.findAction("deploy.init", "denied")
	require.True(t, ok, "deploy.init denied line emitted")
	assert.Equal(t, "mallory", denied["actor"])
}
