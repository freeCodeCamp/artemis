package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthZ_NoAuthRequired_ReturnsOK(t *testing.T) {
	h := &Handlers{}

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.HealthZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ok":true}`, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}
