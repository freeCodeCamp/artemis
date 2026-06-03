package main

import (
	"context"
	"errors"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

const (
	topicSiteReconcile = "site.reconcile"
	cronTombstonePurge = "0 3 * * *"
)

func gcWorkflowDefs(gcw *gcWiring, dryRun bool) []worker.WorkflowDef {
	return []worker.WorkflowDef{
		{
			Name:           worker.WorkflowGCSite,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{pg.TopicSiteChanged},
			Handler: func(ctx context.Context, input map[string]any) error {
				site, err := siteFromInput(input)
				if err != nil {
					return err
				}
				if _, err := gcw.SiteGC.Run(ctx, site, dryRun); err != nil {
					observability.CaptureBackground("gc.site.run", err)
					return err
				}
				return nil
			},
		},
		{
			Name: worker.WorkflowTombstonePurge,
			Cron: []string{cronTombstonePurge},
			Handler: func(ctx context.Context, _ map[string]any) error {
				if _, err := gcw.Purge.Run(ctx, dryRun); err != nil {
					observability.CaptureBackground("tombstone.purge", err)
					return err
				}
				return nil
			},
		},
		{
			Name:           worker.WorkflowReconcile,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{topicSiteReconcile},
			Handler: func(ctx context.Context, input map[string]any) error {
				site, err := siteFromInput(input)
				if err != nil {
					return err
				}
				if _, err := gcw.Reconciler.ReconcileSite(ctx, site); err != nil {
					observability.CaptureBackground("reconcile.run", err)
					return err
				}
				return nil
			},
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

func registerGCWorkflows(rt *worker.Runtime, gcw *gcWiring, dryRun bool) error {
	for _, def := range gcWorkflowDefs(gcw, dryRun) {
		if err := rt.Register(def); err != nil {
			return err
		}
	}
	return nil
}
