package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opPendingSweep        = "pending.sweep"
	pendingSweepSiteLimit = 50
)

type pendingSiteSource interface {
	SitesWithExpiredPending(ctx context.Context, before time.Time, limit int) ([]sitekey.Dirname, error)
}

func runPendingSweep(ctx context.Context, src pendingSiteSource, g *gc.SiteGC, dryRun bool) error {
	if src == nil || g == nil || g.Pending == nil || g.PendingIDs == nil {
		slog.WarnContext(ctx, "gc.pending-sweep.unwired",
			"detail", "no pending sweep ran; abandoned deploy rows accumulate while this cron reports a clean night")
		return nil
	}

	sites, err := src.SitesWithExpiredPending(ctx, g.Now().Add(-g.Policy.Grace), pendingSweepSiteLimit+1)
	if err != nil {
		observability.CaptureBackground(opPendingSweep, err)
		return err
	}
	if len(sites) == 0 {
		return nil
	}
	capped := len(sites) > pendingSweepSiteLimit
	if capped {
		slog.WarnContext(ctx, "gc.pending-sweep.capped", "sites", len(sites), "limit", pendingSweepSiteLimit,
			"reason", "collecting the first sites this run; the remainder waits for the next night")
		sites = sites[:pendingSweepSiteLimit]
	}

	var errs []error
	var tombstoned, skipped, visited int
	for _, site := range sites {
		if err := ctx.Err(); err != nil {
			slog.WarnContext(ctx, "gc.pending-sweep.budget_exhausted", "site", site,
				"detail", "the run budget expired mid-sweep; the remaining sites wait for the next night")
			errs = append(errs, err)
			break
		}
		res, err := g.SweepPending(ctx, site, dryRun)
		if err != nil {
			slog.WarnContext(ctx, "gc.pending-sweep.site_failed", "site", site, "err", err)
			errs = append(errs, err)
			continue
		}
		visited++
		tombstoned += len(res.Tombstoned)
		skipped += len(res.SkippedNotPending)
	}

	slog.InfoContext(ctx, "gc.pending-sweep.done",
		"sites", len(sites), "visited", visited, "tombstoned", tombstoned,
		"skippedNotPending", skipped, "capped", capped, "dryRun", dryRun)

	if joined := errors.Join(errs...); joined != nil {
		observability.CaptureBackground(opPendingSweep, joined)
		return joined
	}
	return nil
}
