package worker

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

var errFail = errors.New("relay boom")

func TestObs(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveRun("gc-site", "ok")
	m.ObserveRun("gc-site", "failed")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.WorkflowRuns.WithLabelValues("gc-site", "ok")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.WorkflowFailures.WithLabelValues("gc-site")),
		"a failed outcome also bumps the failure counter")

	m.ObserveDeadLetter("gc-site")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.DeadLettered.WithLabelValues("gc-site")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.DLQDepth), "dead-letter raises DLQ depth")

	m.SetQueueDepth("gc-site", 42)
	assert.EqualValues(t, 42, testutil.ToFloat64(m.QueueDepth.WithLabelValues("gc-site")))

	m.SetDLQDepth(0)
	assert.EqualValues(t, 0, testutil.ToFloat64(m.DLQDepth), "operator drained the DLQ")

	m.ObserveRelay(7, nil)
	assert.EqualValues(t, 7, testutil.ToFloat64(m.RelayPublished))
	assert.EqualValues(t, 0, testutil.ToFloat64(m.RelayFailures))

	m.ObserveRelay(3, errFail)
	assert.EqualValues(t, 10, testutil.ToFloat64(m.RelayPublished), "partial drain still counts what published")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.RelayFailures))
}

func TestObs_NilSafe(t *testing.T) {
	var m *Metrics
	m.ObserveRun("x", "ok")
	m.ObserveDeadLetter("x")
	m.SetQueueDepth("x", 1)
	m.SetDLQDepth(1)
	m.ObserveRelay(1, nil)
}
