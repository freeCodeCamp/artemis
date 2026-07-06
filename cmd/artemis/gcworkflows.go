package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/getsentry/sentry-go"
)

var captureCheckIn = sentry.CaptureCheckIn

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
	topicSiteReconcile         = "site.reconcile"
	workflowReconcileScheduler = "reconcile-scheduler"
	cronTombstonePurge         = "0 3 * * *"
	cronReconcile              = "0 4 * * *"
	relayInterval              = 5 * time.Second
)

func runRelayLoop(ctx context.Context, relay *worker.Relay, interval time.Duration, metrics *worker.Metrics) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rctx := telemetry.NewContext(ctx, telemetry.NewRun(newRunID()))
			start := time.Now()
			n, err := relay.RunOnce(rctx)
			metrics.ObserveRelay(n, err)
			metrics.ObserveRelayDuration(time.Since(start).Seconds())
			if err != nil {
				slog.ErrorContext(rctx, "relay.run", "err", err)
				observability.CaptureBackground("relay.run", err)
			}
		}
	}
}

func observeWorkflow(metrics *worker.Metrics, name string, fn worker.Handler) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		ctx = telemetry.NewContext(ctx, telemetry.NewRun(newRunID()))
		slog.InfoContext(ctx, "workflow.start", "workflow", name)
		start := time.Now()
		err := fn(ctx, input)
		metrics.ObserveDuration(name, time.Since(start).Seconds())
		outcome := "ok"
		if err != nil {
			outcome = "failed"
			slog.ErrorContext(ctx, "workflow.failed", "workflow", name, "err", err)
		} else {
			slog.InfoContext(ctx, "workflow.done", "workflow", name)
		}
		metrics.ObserveRun(name, outcome)
		return err
	}
}

func gcWorkflowDefs(gcw *gcWiring, dryRun bool, metrics *worker.Metrics, publisher worker.Publisher, reconcileSites func() []string) []worker.WorkflowDef {
	return []worker.WorkflowDef{
		{
			Name: workflowReconcileScheduler,
			Cron: []string{cronReconcile},
			Handler: withCheckIn(workflowReconcileScheduler, cronReconcile, observeWorkflow(metrics, workflowReconcileScheduler, func(ctx context.Context, _ map[string]any) error {
				var firstErr error
				for _, site := range reconcileSites() {
					payload, err := json.Marshal(map[string]string{"site": site})
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					if err := publisher.Publish(ctx, topicSiteReconcile, payload); err != nil {
						observability.CaptureBackground("reconcile.schedule", err)
						if firstErr == nil {
							firstErr = err
						}
					}
				}
				return firstErr
			})),
		},
		{
			Name:           worker.WorkflowGCSite,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{pg.TopicSiteChanged},
			Handler: observeWorkflow(metrics, worker.WorkflowGCSite, func(ctx context.Context, input map[string]any) error {
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
			Name: worker.WorkflowTombstonePurge,
			Cron: []string{cronTombstonePurge},
			Handler: withCheckIn(worker.WorkflowTombstonePurge, cronTombstonePurge, observeWorkflow(metrics, worker.WorkflowTombstonePurge, func(ctx context.Context, _ map[string]any) error {
				if _, err := gcw.Purge.Run(ctx, dryRun); err != nil {
					observability.CaptureBackground("tombstone.purge", err)
					return err
				}
				return nil
			})),
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

func registerGCWorkflows(rt *worker.Runtime, gcw *gcWiring, dryRun bool, metrics *worker.Metrics, publisher worker.Publisher, reconcileSites func() []string) error {
	for _, def := range gcWorkflowDefs(gcw, dryRun, metrics, publisher, reconcileSites) {
		if err := rt.Register(def); err != nil {
			return err
		}
	}
	return nil
}
