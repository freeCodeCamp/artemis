package handler

import (
	"context"
	"encoding/json"
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

func TestDeployUpload_LogsSuccessWithActor(t *testing.T) {
	cap := captureAccessLog(t)
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, &fakeSites{bySite: map[string][]string{"www": {"team-a"}}}, store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		[]byte("<h1>hi</h1>"),
		map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "text/html"},
		RequestID(h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload))).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	m, ok := cap.findAction("deploy.upload", "success")
	require.True(t, ok, "deploy.upload success line emitted")
	assert.Equal(t, "alice", m["actor"])
	assert.Equal(t, "www", m["site"])
	assert.Equal(t, deployID, m["deployId"])
}

func TestDeployFinalize_LogsSuccessWithActorAndBytes(t *testing.T) {
	cap := captureAccessLog(t)
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, &fakeSites{bySite: map[string][]string{"www": {"team-a"}}}, store)

	deployID := "20260420-141522-abc1234"
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("<h1>hi</h1>")
	store.objects[prefix+"assets/app.js"] = []byte("//js")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html", "assets/app.js"}})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		RequestID(h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize))).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	m, ok := cap.findAction("deploy.finalize", "success")
	require.True(t, ok, "deploy.finalize success line emitted")
	assert.Equal(t, "alice", m["actor"])
	assert.Equal(t, deployID, m["deployId"])
	assert.NotEqual(t, "0", m["bytes"], "deployBytes carried on the success line")
}
