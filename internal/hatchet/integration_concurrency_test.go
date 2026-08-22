//go:build integration

package hatchet_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestR3SameSiteNeverConcurrent(t *testing.T) {
	obs := newObserver()
	h := startHarness(t, obs, map[string]worker.Handler{
		harnessWorkflowA: instrumented(obs, 1500*time.Millisecond, nil),
	})

	const site = "r3-same-site"
	h.fire(t, harnessWorkflowA, site)
	h.fire(t, harnessWorkflowA, site)
	h.fire(t, harnessWorkflowA, site)

	h.waitStarts(t, site, 3, "three events for one site never all ran")

	require.LessOrEqual(t, h.observed.peakConcurrency(site), 1,
		"two events for the same site must never run concurrently")
}

func TestR3DistinctSitesRunConcurrent(t *testing.T) {
	obs := newObserver()
	h := startHarness(t, obs, map[string]worker.Handler{
		harnessWorkflowB: instrumented(obs, 1500*time.Millisecond, nil),
	})

	siteA := "r3-distinct-a"
	siteB := "r3-distinct-b"
	h.fire(t, harnessWorkflowB, siteA)
	h.fire(t, harnessWorkflowB, siteB)

	h.waitStarts(t, siteA, 1, "site A never ran")
	h.waitStarts(t, siteB, 1, "site B never ran")

	require.GreaterOrEqual(t, h.observed.peakGlobalConcurrency(), 2,
		"distinct sites must overlap in execution, not merely both start eventually")
}
