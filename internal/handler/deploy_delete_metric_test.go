package handler

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteDeployDelete_IncrementsManualTrigger(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.DeploysTombstoned = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "artemis_gc_deploys_tombstoned_total",
		Help: "shared with gc.Metrics; labelled by trigger.",
	}, []string{"trigger"})
	reg.MustRegister(m.DeploysTombstoned)
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)
	SetMetrics(m)

	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Equal(t, float64(1), testutil.ToFloat64(m.DeploysTombstoned.WithLabelValues("manual")),
		"manual deploy delete is observable under trigger=manual")
	assert.Equal(t, float64(0), testutil.ToFloat64(m.DeploysTombstoned.WithLabelValues("scheduled")))
}
