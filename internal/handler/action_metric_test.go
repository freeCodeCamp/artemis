package handler

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestLogAction_IncrementsActionMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)
	SetMetrics(m)

	h := &Handlers{}
	h.logAction(context.Background(), "site.promote", "success")
	h.logAction(context.Background(), "site.promote", "success")
	h.logAction(context.Background(), "deploy.init", "denied")

	assert.Equal(t, float64(2), testutil.ToFloat64(m.ActionTotal.WithLabelValues("site.promote", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(m.ActionTotal.WithLabelValues("deploy.init", "denied")))
}
