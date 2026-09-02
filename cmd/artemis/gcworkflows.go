package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/getsentry/sentry-go"
)

var (
	captureCheckIn    = sentry.CaptureCheckIn
	captureBackground = observability.CaptureBackground
)

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
	outboxStuckAfter     = 15 * time.Minute
	outboxProbeEvery     = 12
	outboxProbeTimeout   = 5 * time.Second
	opOutboxBacklog      = "outbox.backlog"
)

type driftSweeper func(ctx context.Context) (sweepResult, error)

type outboxBacklogSource interface {
	OutboxBacklog(ctx context.Context) (int, time.Duration, error)
}

type backlogState struct{ paged bool }

func (s *backlogState) observe(stuck bool) bool {
	if !stuck {
		s.paged = false
		return false
	}
	if s.paged {
		return false
	}
	s.paged = true
	return true
}

func runRelayLoop(ctx context.Context, relay *worker.Relay, backlog outboxBacklogSource, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var state backlogState
	ticks := 0
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
			ticks++
			if backlog == nil || ticks%outboxProbeEvery != 0 {
				continue
			}
			observeOutboxBacklog(rctx, backlog, &state)
		}
	}
}

func observeOutboxBacklog(ctx context.Context, backlog outboxBacklogSource, state *backlogState) {
	probeCtx, cancel := context.WithTimeout(ctx, outboxProbeTimeout)
	defer cancel()
	count, oldest, err := backlog.OutboxBacklog(probeCtx)
	if err != nil {
		slog.WarnContext(ctx, "outbox.backlog.unreadable", "err", err)
		return
	}
	if !state.observe(count > 0 && oldest >= outboxStuckAfter) {
		return
	}
	stuck := fmt.Errorf(
		"%d outbox events are unpublished and the oldest has waited %s (>= %s): the relay reports "+
			"success while draining nothing, so every site.changed behind it never reaches hatchet "+
			"and gc-site never runs",
		count, oldest.Round(time.Second), outboxStuckAfter)
	slog.ErrorContext(ctx, "outbox.backlog.stuck", "count", count, "oldest", oldest, "err", stuck)
	captureBackground(opOutboxBacklog, stuck)
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
				var errs []error
				if _, err := gcw.Purge.Run(ctx, dryRun); err != nil {
					observability.CaptureBackground("tombstone.purge", err)
					errs = append(errs, err)
				}
				if err := purgeOutbox(ctx, gcw.Outbox, gcw.OutboxRetention, dryRun); err != nil {
					errs = append(errs, err)
				}
				if err := runPendingSweep(ctx, gcw.PendingSites, gcw.SiteGC, dryRun); err != nil {
					errs = append(errs, err)
				}
				if err := runReservationSweep(ctx, gcw.Reservations, gcw.NameReleaser, gcw.Reclaim, time.Now, dryRun); err != nil {
					errs = append(errs, err)
				}
				return errors.Join(errs...)
			})),
		},
	}
}

func siteFromInput(input map[string]any) (sitekey.Dirname, error) {
	s, ok := input["site"].(string)
	if !ok || s == "" {
		return "", errors.New("workflow input missing site")
	}
	return sitekey.Dirname(s), nil
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

func storageSiteNames(slugs []sitekey.Slug, tmpl handler.DeployPrefixTemplate) []sitekey.Dirname {
	if len(slugs) == 0 {
		return nil
	}
	names := make([]sitekey.Dirname, 0, len(slugs))
	for _, s := range slugs {
		names = append(names, tmpl.SiteDirname(s))
	}
	return names
}

// outboxPurgeBatch bounds one night's delete. A backlog larger than
// this drains over successive runs rather than holding a single long
// transaction open against the table the relay writes to.
const outboxPurgeBatch = 5000

func purgeOutbox(ctx context.Context, p outboxPurger, retention time.Duration, dryRun bool) error {
	if p == nil || retention <= 0 {
		return nil
	}
	before := time.Now().UTC().Add(-retention)
	n, err := p.PurgeOutbox(ctx, before, outboxPurgeBatch, dryRun)
	if err != nil {
		observability.CaptureBackground("outbox.purge", err)
		return fmt.Errorf("outbox purge: %w", err)
	}
	slog.InfoContext(ctx, "outbox.purged", "rows", n, "before", before, "dryRun", dryRun)
	if n == outboxPurgeBatch && !dryRun {
		slog.WarnContext(ctx, "outbox.purge.capped", "rows", n, "batch", outboxPurgeBatch)
		observability.CaptureBackground("outbox.purge.capped",
			fmt.Errorf("outbox purge hit its %d-row ceiling: a backlog survived the run", outboxPurgeBatch))
	}
	return nil
}
