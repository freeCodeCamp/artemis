//go:build integration

package hatchet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestR4PoisonDeadLettersWithoutBlockingKey(t *testing.T) {
	obs := newObserver()

	var poisonSeen, healthySeen int
	poison := func(ctx context.Context, input map[string]any) error {
		site := siteOf(input)
		obs.enter(site)
		defer obs.leave(site)
		poisonSeen++
		return errors.New("poison: deliberate failure for dead-letter")
	}
	healthy := func(ctx context.Context, input map[string]any) error {
		site := siteOf(input)
		obs.enter(site)
		defer obs.leave(site)
		healthySeen++
		return nil
	}

	h := startHarness(t, obs, map[string]worker.Handler{
		worker.WorkflowRollback: poison,
		worker.WorkflowFinalize: healthy,
	})

	const site = "r4-poison-site"
	h.fire(t, worker.WorkflowRollback, site)
	h.waitStarts(t, site, 1)

	time.Sleep(2 * time.Second)

	h.fire(t, worker.WorkflowFinalize, site)
	h.waitStarts(t, site, 2)

	if poisonSeen < 1 {
		t.Fatalf("poison workflow never ran")
	}
	if healthySeen < 1 {
		t.Fatalf("healthy workflow on same key never ran: poison left the concurrency key blocked")
	}
}
