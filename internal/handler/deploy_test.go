package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withChiRoute wraps the handler in a chi router so URL params populate.
func withChiRoute(method, pattern, target string, body []byte, headers map[string]string, fn http.HandlerFunc, ctx context.Context) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(method, pattern, fn)

	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func standardSites() *fakeSites {
	return &fakeSites{bySite: map[string][]string{
		"www":   {"team-eng", "team-platform"},
		"learn": {"team-eng"},
	}}
}

func TestDeployInit_Success(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(DeployInitRequest{Site: "www", SHA: "abc1234567"})
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	h.DeployInit(w, r)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got DeployInitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.NotEmpty(t, got.JWT)
	assert.Equal(t, "20260420-141522-abc1234", got.DeployID)
	assert.NotEmpty(t, got.ExpiresAt)
}

func TestDeployInit_RejectsMissingSiteOrSHA(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(DeployInitRequest{Site: "", SHA: ""})
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	h.DeployInit(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeployInit_RejectsUserNotOnAuthorizedTeam(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {"team-other": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(DeployInitRequest{Site: "www", SHA: "abc1234"})
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "carol", "tok"))
	w := httptest.NewRecorder()
	h.DeployInit(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestDeployInit_RejectsUnregisteredSite(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(DeployInitRequest{Site: "ghost-site", SHA: "abc1234"})
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	h.DeployInit(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestDeployUpload_RejectsWrongDeployID(t *testing.T) {
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), newFakeR2())

	tok, _, err := jwt.Sign("alice", "www", "20260420-141522-abc1234")
	require.NoError(t, err)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/wrong-deploy/upload?path=index.html",
		[]byte("hello"),
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestDeployUpload_StoresInR2(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		[]byte("<h1>hi</h1>"),
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	got := store.objects["www/deploys/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.Equal(t, "<h1>hi</h1>", string(got))
}

func TestDeployUpload_RejectsTraversalPath(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=../escape.html",
		[]byte("x"),
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDeployFinalize_VerifyThenAlias(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("<h1>hi</h1>")
	store.objects[prefix+"assets/app.js"] = []byte("//js")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{
		Mode:  "preview",
		Files: []string{"index.html", "assets/app.js"},
	})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	store.mu.Lock()
	alias := store.aliases["www/preview"]
	store.mu.Unlock()
	assert.Equal(t, deployID, alias)
}

func TestDeployFinalize_VerifyMissing_DoesNotWriteAlias(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("<h1>hi</h1>")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{
		Mode:  "preview",
		Files: []string{"index.html", "assets/app.js"},
	})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.True(t, strings.Contains(w.Body.String(), "missing"))

	store.mu.Lock()
	_, hasAlias := store.aliases["www/preview"]
	store.mu.Unlock()
	assert.False(t, hasAlias, "alias must NOT be written on verify failure")
}

func TestDeployFinalize_RejectsExpiredJWT(t *testing.T) {
	// Build a real signer with 1ms TTL so the JWT is already expired.
	st := standardSites()
	store := newFakeR2()
	h, _ := newTestHandlers(t, &fakeGH{}, st, store)

	// override JWT signer with very short TTL signer
	short, err := newShortLivedSigner()
	require.NoError(t, err)
	h.JWT = short

	deployID := "20260420-141522-abc1234"
	tok, _, err := short.Sign("alice", "www", deployID)
	require.NoError(t, err)
	// Wait long enough for expiry.
	sleepUntilExpired()

	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview"})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
