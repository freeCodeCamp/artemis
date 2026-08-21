//go:build integration

package hatchet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestR4FailedRunDoesNotBlockTheConcurrencyKey(t *testing.T) {
	obs := newObserver()

	h := startHarness(t, obs, map[string]worker.Handler{
		harnessWorkflowC: instrumented(obs, 0, errors.New("poison: deliberate failure")),
	})

	const site = "r4-poison-site"
	h.fire(t, harnessWorkflowC, site)
	h.waitStarts(t, site, 1, "the poison run never started")

	time.Sleep(2 * time.Second)

	h.fire(t, harnessWorkflowC, site)
	h.waitStarts(t, site, 2,
		"a second run of the same workflow on the same site never started: the failed run held its concurrency slot")
}
