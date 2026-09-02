//go:build integration

package hatchet_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	hatchetadapter "github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

const (
	multiStrategyWorkflow = "artemis.it.multi"
	multiStrategyMaxRuns  = 2
	multiStrategySites    = 5
	multiStrategyHold     = 3 * time.Second
)

func TestR6TwoConcurrencyStrategiesBothBindAndQueue(t *testing.T) {
	requireEngine(t)
	obs := newObserver()
	var completed atomic.Int32
	suffix := shortID()
	topic := scopedTopic(multiStrategyWorkflow, suffix)

	adapter := hatchetadapter.New(hatchetadapter.Config{WorkerName: "artemis-it-multi-" + suffix})
	require.NoError(t, adapter.Register(worker.WorkflowDef{
		Name:             topic,
		ConcurrencyKey:   worker.ConcurrencyKeySite,
		ExtraConcurrency: []worker.ConcurrencyLimit{{Key: worker.ConcurrencyKeyAction, MaxRuns: multiStrategyMaxRuns}},
		EventTriggers:    []string{topic},
		Handler: func(ctx context.Context, input map[string]any) error {
			site := siteOf(input)
			obs.enter(site)
			defer obs.leave(site)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(multiStrategyHold):
				completed.Add(1)
				return nil
			}
		},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- adapter.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
		}
	})
	waitPublishable(t, adapter)
	h := &harness{pub: adapter, observed: obs, suffix: suffix, startErr: errCh}

	sites := make([]string, multiStrategySites)
	for i := range sites {
		sites[i] = fmt.Sprintf("r6-multi-%02d", i)
		payload := []byte(fmt.Sprintf(`{"site":%q,"action":"reclaim"}`, sites[i]))
		require.NoError(t, adapter.Publish(context.Background(), topic, payload))
	}

	for _, site := range sites {
		h.waitStarts(t, site, 1, "every event must reach the worker; a missing concurrency key fails the run before it starts")
	}
	deadline := time.Now().Add(runReadyTimeout)
	for completed.Load() < multiStrategySites && time.Now().Before(deadline) {
		time.Sleep(pollInterval)
	}

	require.EqualValues(t, multiStrategySites, completed.Load(),
		"GROUP_ROUND_ROBIN must queue the excess, never cancel it; a cancelled run leaves a reclaim half done")
	require.LessOrEqual(t, obs.peakGlobalConcurrency(), multiStrategyMaxRuns,
		"the action-wide strategy bounds runs across distinct sites; each reclaim run holds one advisory-lock connection, so this bound is the connection cap")
	require.Equal(t, multiStrategyMaxRuns, obs.peakGlobalConcurrency(),
		"distinct sites run concurrently (R3), so the peak must reach the action-wide cap; a peak of 1 means the per-site strategy alone was registered")
}
