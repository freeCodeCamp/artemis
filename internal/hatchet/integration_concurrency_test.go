//go:build integration

package hatchet_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	hatchetadapter "github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestR2WorkerRegistersPerSiteConcurrency(t *testing.T) {
	requireEngine(t)

	adapter := hatchetadapter.New(hatchetadapter.Config{WorkerName: "artemis-it-" + shortID()})
	for _, def := range deployDefs(newObserver(), nil) {
		require.NoError(t, adapter.Register(def))
	}

	regd := adapter.Registered()
	require.Len(t, regd, 3)

	want := map[string]bool{
		worker.WorkflowFinalize: false,
		worker.WorkflowPromote:  false,
		worker.WorkflowRollback: false,
	}
	for _, def := range regd {
		_, ok := want[def.Name]
		require.True(t, ok, "unexpected workflow %q", def.Name)
		require.Equal(t, worker.ConcurrencyKeySite, def.ConcurrencyKey,
			"workflow %q must key concurrency on site", def.Name)
		require.Contains(t, def.EventTriggers, def.Name)
		want[def.Name] = true
	}
	for name, seen := range want {
		require.True(t, seen, "workflow %q not registered", name)
	}
}

func TestR3SameSiteNeverConcurrent(t *testing.T) {
	obs := newObserver()
	h := startHarness(t, obs, map[string]worker.Handler{
		worker.WorkflowFinalize: instrumented(obs, 1500*time.Millisecond, nil),
	})

	const site = "r3-same-site"
	h.fire(t, worker.WorkflowFinalize, site)
	h.fire(t, worker.WorkflowFinalize, site)
	h.fire(t, worker.WorkflowFinalize, site)

	h.waitStarts(t, site, 3)

	require.LessOrEqual(t, h.observed.peakConcurrency(site), 1,
		"two events for the same site must never run concurrently")
}

func TestR3DistinctSitesRunConcurrent(t *testing.T) {
	obs := newObserver()
	h := startHarness(t, obs, map[string]worker.Handler{
		worker.WorkflowPromote: instrumented(obs, 1500*time.Millisecond, nil),
	})

	siteA := "r3-distinct-a"
	siteB := "r3-distinct-b"
	h.fire(t, worker.WorkflowPromote, siteA)
	h.fire(t, worker.WorkflowPromote, siteB)

	h.waitStarts(t, siteA, 1)
	h.waitStarts(t, siteB, 1)
}
