package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

const (
	topicSiteReconcile = "site.reconcile"
	cronTombstonePurge = "0 3 * * *"
	relayInterval      = 5 * time.Second
)

func runRelayLoop(ctx context.Context, relay *worker.Relay, interval time.Duration, metrics *worker.Metrics) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := relay.RunOnce(ctx)
			metrics.ObserveRelay(n, err)
			if err != nil {
				slog.Error("relay.run", "err", err)
				observability.CaptureBackground("relay.run", err)
			}
		}
	}
}

func observeWorkflow(metrics *worker.Metrics, name string, fn worker.Handler) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		err := fn(ctx, input)
		outcome := "ok"
		if err != nil {
			outcome = "failed"
		}
		metrics.ObserveRun(name, outcome)
		return err
	}
}

func gcWorkflowDefs(gcw *gcWiring, dryRun bool, metrics *worker.Metrics) []worker.WorkflowDef {
	return []worker.WorkflowDef{
		{
			Name:           worker.WorkflowGCSite,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{pg.TopicSiteChanged},
			Handler: observeWorkflow(metrics, worker.WorkflowGCSite, func(ctx context.Context, input map[string]any) error {
				site, err := siteFromInput(input)
				if err != nil {
					return err
				}
				run := func() error {
					if _, err := gcw.SiteGC.Run(ctx, site, dryRun); err != nil {
						observability.CaptureBackground("gc.site.run", err)
						return err
					}
					return nil
				}
				if dryRun {
					return run()
				}
				return gcw.Repo.WithSiteLock(ctx, site, run)
			}),
		},
		{
			Name: worker.WorkflowTombstonePurge,
			Cron: []string{cronTombstonePurge},
			Handler: observeWorkflow(metrics, worker.WorkflowTombstonePurge, func(ctx context.Context, _ map[string]any) error {
				if _, err := gcw.Purge.Run(ctx, dryRun); err != nil {
					observability.CaptureBackground("tombstone.purge", err)
					return err
				}
				return nil
			}),
		},
		{
			Name:           worker.WorkflowReconcile,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{topicSiteReconcile},
			Handler: observeWorkflow(metrics, worker.WorkflowReconcile, func(ctx context.Context, input map[string]any) error {
				site, err := siteFromInput(input)
				if err != nil {
					return err
				}
				if _, err := gcw.Reconciler.ReconcileSite(ctx, site); err != nil {
					observability.CaptureBackground("reconcile.run", err)
					return err
				}
				return nil
			}),
		},
	}
}

func siteFromInput(input map[string]any) (string, error) {
	s, ok := input["site"].(string)
	if !ok || s == "" {
		return "", errors.New("workflow input missing site")
	}
	return s, nil
}

func registerGCWorkflows(rt *worker.Runtime, gcw *gcWiring, dryRun bool, metrics *worker.Metrics) error {
	for _, def := range gcWorkflowDefs(gcw, dryRun, metrics) {
		if err := rt.Register(def); err != nil {
			return err
		}
	}
	return nil
}
