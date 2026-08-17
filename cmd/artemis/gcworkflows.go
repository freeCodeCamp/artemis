package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/getsentry/sentry-go"
)

var captureCheckIn = sentry.CaptureCheckIn
var captureBackground = observability.CaptureBackground

func onConcurrentMigrateErr(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		slog.WarnContext(ctx, "pg.migrate.concurrent.aborted", "err", err)
		return
	}
	captureBackground("pg.migrate.concurrent", err)
}

func withCheckIn(slug, cron string, fn worker.Handler) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		cfg := &sentry.MonitorConfig{Schedule: sentry.CrontabSchedule(cron)}
		id := captureCheckIn(&sentry.CheckIn{
			MonitorSlug: slug,
			Status:      sentry.CheckInStatusInProgress,
		}, cfg)
		start := time.Now()
		err := fn(ctx, input)
		status := sentry.CheckInStatusOK
		if err != nil {
			status = sentry.CheckInStatusError
		}
		var cid sentry.EventID
		if id != nil {
			cid = *id
		}
		captureCheckIn(&sentry.CheckIn{
			ID:          cid,
			MonitorSlug: slug,
			Status:      status,
			Duration:    time.Since(start),
		}, cfg)
		return err
	}
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const (
	cronTombstonePurge   = "0 3 * * *"
	cronDriftDetect      = "0 4 * * *"
	driftDetectRunBudget = 30 * time.Minute
	gcRunBudget          = 30 * time.Minute
	relayInterval        = 5 * time.Second
)

type driftSweeper func(ctx context.Context) (sweepResult, error)

func runRelayLoop(ctx context.Context, relay *worker.Relay, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rctx := telemetry.NewContext(ctx, telemetry.NewRun(newRunID()))
			if _, err := relay.RunOnce(rctx); err != nil {
				slog.ErrorContext(rctx, "relay.run", "err", err)
				observability.CaptureBackground("relay.run", err)
			}
		}
	}
}

func observeWorkflow(name string, fn worker.Handler) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		ctx = telemetry.NewContext(ctx, telemetry.NewRun(newRunID()))
		slog.InfoContext(ctx, "workflow.start", "workflow", name)
		err := fn(ctx, input)
		if err != nil {
			slog.ErrorContext(ctx, "workflow.failed", "workflow", name, "err", err)
		} else {
			slog.InfoContext(ctx, "workflow.done", "workflow", name)
		}
		return err
	}
}

func gcWorkflowDefs(gcw *gcWiring, dryRun bool, sweepDrift driftSweeper) []worker.WorkflowDef {
	return []worker.WorkflowDef{
		{
			Name:             worker.WorkflowDriftDetect,
			Cron:             []string{cronDriftDetect},
			ExecutionTimeout: driftDetectRunBudget,
			Handler: withCheckIn(worker.WorkflowDriftDetect, cronDriftDetect, observeWorkflow(worker.WorkflowDriftDetect, func(ctx context.Context, _ map[string]any) error {
				res, err := sweepDrift(ctx)
				if err != nil {
					captureBackground(opDriftSweep, err)
					return err
				}
				return alertOnDrift(ctx, res)
			})),
		},
		{
			Name:             worker.WorkflowGCSite,
			ConcurrencyKey:   worker.ConcurrencyKeySite,
			EventTriggers:    []string{pg.TopicSiteChanged},
			ExecutionTimeout: gcRunBudget,
			Handler: observeWorkflow(worker.WorkflowGCSite, func(ctx context.Context, input map[string]any) error {
				site, err := siteFromInput(input)
				if err != nil {
					return err
				}
				if _, err := gcw.SiteGC.Run(ctx, site, dryRun); err != nil {
					observability.CaptureBackground("gc.site.run", err)
					return err
				}
				return nil
			}),
		},
		{
			Name:             worker.WorkflowTombstonePurge,
			Cron:             []string{cronTombstonePurge},
			ExecutionTimeout: gcRunBudget,
			Handler: withCheckIn(worker.WorkflowTombstonePurge, cronTombstonePurge, observeWorkflow(worker.WorkflowTombstonePurge, func(ctx context.Context, _ map[string]any) error {
				if _, err := gcw.Purge.Run(ctx, dryRun); err != nil {
					observability.CaptureBackground("tombstone.purge", err)
					return err
				}
				return nil
			})),
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

type workflowRegistrar interface {
	Register(worker.WorkflowDef) error
}

func registerGCWorkflows(rt workflowRegistrar, gcw *gcWiring, dryRun bool, sweepDrift driftSweeper) error {
	for _, def := range gcWorkflowDefs(gcw, dryRun, sweepDrift) {
		if err := rt.Register(def); err != nil {
			return err
		}
	}
	return nil
}

func storageSiteNames(slugs []string, tmpl handler.DeployPrefixTemplate) []string {
	if len(slugs) == 0 {
		return nil
	}
	names := make([]string, 0, len(slugs))
	for _, s := range slugs {
		names = append(names, tmpl.SiteDirname(s))
	}
	return names
}
