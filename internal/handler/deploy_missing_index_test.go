package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasIndexHTML(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"bare root", []string{"index.html"}, true},
		{"root among others", []string{"index.html", "assets/app.js"}, true},
		{"root with framework markers still served", []string{"index.html", ".next/BUILD_ID", "BUILD_ID"}, true},
		{"nested only", []string{"assets/index.html", "app.js"}, false},
		{"dot-slash prefix is a different key", []string{"./index.html"}, false},
		{"leading slash is a different key", []string{"/index.html"}, false},
		{"uppercase is a different key", []string{"INDEX.HTML"}, false},
		{"no index at all", []string{"app.js", "style.css"}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hasIndexHTML(c.files))
		})
	}
}

func TestLooksLikeFrameworkBuild(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"next bare-root (the incident)", []string{"BUILD_ID", "build-manifest.json", "server/app.js"}, true},
		{"next prefixed", []string{".next/BUILD_ID", "index.html"}, true},
		{"nitro output", []string{"nitro.json", "server/index.mjs"}, true},
		{"sveltekit client assets", []string{"_app/immutable/chunks/x.js"}, true},
		{"nitro prefixed", []string{".output/server/index.mjs"}, true},
		{"nuxt cache prefixed", []string{".nuxt/dist/x.js"}, true},
		{"windows backslash prefixed", []string{`.next\BUILD_ID`, `.next\build-manifest.json`}, true},
		{"single next marker is not enough", []string{"BUILD_ID"}, false},
		{"plain static site", []string{"index.html", "app.js", "style.css"}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, looksLikeFrameworkBuild(c.files))
		})
	}
}

func finalizeMissingIndex(t *testing.T, store *fakeR2, files []string) *httptest.ResponseRecorder {
	t.Helper()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: files})
	return withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
}

func TestDeployFinalize_MissingIndexHTML(t *testing.T) {
	store := newFakeR2()
	w := finalizeMissingIndex(t, store, []string{"assets/app.js", "style.css"})

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "missing_index")

	store.mu.Lock()
	_, hasAlias := store.aliases["www/preview"]
	store.mu.Unlock()
	assert.False(t, hasAlias, "alias must NOT be written when root index.html is missing")
}

func TestDeployFinalize_MissingIndexHTML_NestedIndexNotSufficient(t *testing.T) {
	store := newFakeR2()
	w := finalizeMissingIndex(t, store, []string{"assets/index.html", "app.js"})

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "missing_index")

	store.mu.Lock()
	_, hasAlias := store.aliases["www/preview"]
	store.mu.Unlock()
	assert.False(t, hasAlias, "nested assets/index.html must not satisfy the root index gate")
}

func TestDeployFinalize_MissingIndexHTML_NoMarkerWritten(t *testing.T) {
	store := newFakeR2()
	deployID := "20260420-141522-abc1234"
	prefix := "www/deploys/" + deployID + "/"

	w := finalizeMissingIndex(t, store, []string{"app.js"})
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	store.mu.Lock()
	_, hasMarker := store.objects[prefix+gc.MarkerObjectName]
	store.mu.Unlock()
	assert.False(t, hasMarker, "no deploy marker may be written on a missing_index rejection")
}

func TestDeployFinalize_MissingIndexHTML_FrameworkHint(t *testing.T) {
	store := newFakeR2()
	w := finalizeMissingIndex(t, store, []string{"BUILD_ID", "build-manifest.json", "server/app.js"})

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	body := w.Body.String()
	assert.Contains(t, body, "missing_index")
	assert.Contains(t, body, "static export", "framework-build rejections must carry the static-export hint")
}

func TestDeployFinalize_MissingIndexHTML_NoHintForPlainStatic(t *testing.T) {
	store := newFakeR2()
	w := finalizeMissingIndex(t, store, []string{"styles.css", "logo.png"})

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "static export")
}

func TestDeployFinalize_MissingIndexHTML_ErrCodeRecorded(t *testing.T) {
	cap := captureAccessLog(t)
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"app.js"}})

	router := chi.NewRouter()
	router.Use(RequestID)
	router.Use(AccessLog)
	router.Post("/api/deploy/{deployId}/finalize",
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP)

	req := httptest.NewRequest(http.MethodPost, "/api/deploy/"+deployID+"/finalize", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Equal(t, "missing_index", cap.httpAttr(t, "errCode"))
}
