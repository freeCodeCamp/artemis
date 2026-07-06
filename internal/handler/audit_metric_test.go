package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditEventsTotal_RegisteredWithActionOutcomeLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.AuditEventsTotal.WithLabelValues("site.delete", "success").Inc()
	m.AuditEventsTotal.WithLabelValues("site.delete", "audit_error").Inc()
	m.AuditEventsTotal.WithLabelValues("site.delete", "audit_error").Inc()

	assert.Equal(t, float64(1), testutil.ToFloat64(m.AuditEventsTotal.WithLabelValues("site.delete", "success")))
	assert.Equal(t, float64(2), testutil.ToFloat64(m.AuditEventsTotal.WithLabelValues("site.delete", "audit_error")),
		"audit-write failures are independently observable")

	body := httptest.NewRecorder()
	MetricsHandler(reg).ServeHTTP(body, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, body.Code)
	assert.Contains(t, body.Body.String(), `artemis_audit_events_total{action="site.delete",outcome="audit_error"} 2`)
}
