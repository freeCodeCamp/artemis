//go:build integration

package hatchet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestR4PoisonDeadLettersWithoutBlockingKey(t *testing.T) {
	obs := newObserver()

	h := startHarness(t, obs, map[string]worker.Handler{
		worker.WorkflowRollback: instrumented(obs, 0, errors.New("poison: deliberate failure for dead-letter")),
		worker.WorkflowFinalize: instrumented(obs, 0, nil),
	})

	const site = "r4-poison-site"
	h.fire(t, worker.WorkflowRollback, site)
	h.waitStarts(t, site, 1)

	time.Sleep(2 * time.Second)

	h.fire(t, worker.WorkflowFinalize, site)
	h.waitStarts(t, site, 2)

	require.GreaterOrEqual(t, obs.startsFor(site), 2,
		"healthy workflow on same key never ran: poison left the concurrency key blocked")
}
