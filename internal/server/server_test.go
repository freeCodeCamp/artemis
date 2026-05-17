package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimal stub handlers — we only assert routing wiring here; behaviour is
// covered by the handler package tests.
type stubHandlers struct {
	*handler.Handlers
}

func TestRouter_HealthzNoAuth(t *testing.T) {
	r := New(&handler.Handlers{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ok":true}`, w.Body.String())
}

func TestRouter_WhoamiRequiresBearer(t *testing.T) {
	r := New(&handler.Handlers{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRouter_UploadRequiresJWT(t *testing.T) {
	r := New(&handler.Handlers{}, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/deploy/d1/upload?path=index.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRouter_UnknownRoute_404(t *testing.T) {
	r := New(&handler.Handlers{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/nonsense", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_RequestIDHeaderEcho(t *testing.T) {
	r := New(&handler.Handlers{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "trace-7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "trace-7", w.Header().Get("X-Request-ID"))
}

var _ = stubHandlers{}
