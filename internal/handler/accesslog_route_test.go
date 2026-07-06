package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_RouteIsChiPattern(t *testing.T) {
	cap := captureAccessLog(t)

	router := chi.NewRouter()
	router.Use(RequestID)
	router.Use(AccessLog)
	router.Post("/api/site/{site}/promote", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest(http.MethodPost, "/api/site/www/promote", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "/api/site/{site}/promote", cap.httpAttr(t, "route"))
}
