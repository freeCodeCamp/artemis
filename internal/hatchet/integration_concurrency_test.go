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
	rv := newRendezvous(2)
	h := startHarness(t, obs, map[string]worker.Handler{
		harnessWorkflowB: rendezvousHandler(obs, rv, distinctSitesHold),
	})

	siteA := "r3-distinct-a"
	siteB := "r3-distinct-b"
	h.fire(t, harnessWorkflowB, siteA)
	h.fire(t, harnessWorkflowB, siteB)

	h.waitStarts(t, siteA, 1, "site A never ran")
	h.waitStarts(t, siteB, 1,
		"site B never ran while A held the barrier — if the engine serialised two DIFFERENT sites, "+
			"A cannot release until its hold expires and B cannot start at all, which is the defect this test exists to catch")

	require.GreaterOrEqual(t, h.observed.peakGlobalConcurrency(), 2,
		"each handler holds until the other arrives, so both were in flight at the same instant "+
			"unless the engine serialised two different sites; dispatch skew is absorbed by the barrier "+
			"rather than deciding the result")
}
