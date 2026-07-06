package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_HTTPMetrics_RoutePatternLabelsOnly(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)
	SetMetrics(m)

	router := chi.NewRouter()
	router.Use(RequestID)
	router.Use(AccessLog)
	router.Post("/api/site/{site}/promote", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/site/www/promote", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	got := testutil.ToFloat64(m.HTTPRequestsTotal.WithLabelValues("/api/site/{site}/promote", "POST", "2xx"))
	assert.Equal(t, float64(1), got, "counter labelled by chi route pattern + method + status_class")

	assert.Equal(t, float64(0), testutil.ToFloat64(m.HTTPInFlight), "in-flight gauge returns to zero")

	body := httptest.NewRecorder()
	MetricsHandler(reg).ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := body.Body.String()
	assert.NotContains(t, out, "/api/site/www/promote", "raw path is never a label value")
	assert.NotContains(t, out, "site=\"www\"", "no site label (V3 cardinality guard)")
	assert.NotContains(t, out, "login=", "no login label")
	assert.NotContains(t, out, "deployId=", "no deployId label")
}
