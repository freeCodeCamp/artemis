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

// TestIsCleanRelPath — B22 anchor + tightening: reject ".", absolute,
// traversal, and empty paths. "." passing pre-B22 stored a malformed
// `<deploy-prefix>.` key on R2 (harmless but never spec'd as legal).
func TestIsCleanRelPath(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"index.html", true},
		{"a/b/c.html", true},
		{"foo.bar.baz", true},
		// rejects
		{"", false},
		{".", false},  // B22: was true pre-fix
		{"./", false}, // path.Clean("./") == "." which then != p
		{"..", false},
		{"../escape.html", false},
		{"a/../b", false},
		{"/abs.html", false},
		{"/", false},
	}
	for _, tc := range cases {
		t.Run(tc.p, func(t *testing.T) {
			assert.Equal(t, tc.want, isCleanRelPath(tc.p))
		})
	}
}

// TestDeployUpload_RejectsOversize — B4: uploads exceeding the
// configured cap must short-circuit with 413 + the canonical error
// envelope. Without the cap, an authenticated client can stream
// unbounded bytes into R2 (cost + DoS risk).
func TestDeployUpload_RejectsOversize(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.UploadMaxBytes = 16 // tiny cap for the test

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body := bytes.Repeat([]byte("x"), 1024)
	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "too_large")

	// R2 must NOT have stored the object.
	store.mu.Lock()
	_, stored := store.objects["www/deploys/"+deployID+"/index.html"]
	store.mu.Unlock()
	assert.False(t, stored, "oversize upload must not land in R2")
}

// TestDeployUpload_AllowsAtLimit — boundary: exactly N bytes with N as
// the cap must succeed. Off-by-one regressions in MaxBytesReader caps
// surface here.
func TestDeployUpload_AllowsAtLimit(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.UploadMaxBytes = 16

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body := bytes.Repeat([]byte("y"), 16) // exactly cap
	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
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

// TestDeployFinalize_RequiresFiles — B5: a finalize body with no files
// manifest must NOT flip the alias (today VerifyDeployComplete returns
// nil for an empty expected list, which silently promotes a possibly
// empty deploy and breaks ADR-016's atomic-never-partial invariant).
func TestDeployFinalize_RequiresFiles(t *testing.T) {
	st := standardSites()
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, st, store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview"}) // no Files

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "manifest_required")

	// Alias must NOT have been written.
	store.mu.Lock()
	_, exists := store.aliases["www/preview"]
	store.mu.Unlock()
	assert.False(t, exists, "alias must not flip on empty manifest")
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
