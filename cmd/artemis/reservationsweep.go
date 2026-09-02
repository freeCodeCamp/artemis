package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opReservationSweep    = "reservation.sweep"
	reservationSweepLimit = 50
)

type reclaimableSource interface {
	ReclaimableReservations(ctx context.Context, before time.Time, claimTTL time.Duration, limit int) ([]registry.Reservation, error)
}

type lifecycleEmitter interface {
	EnqueueSiteLifecycle(ctx context.Context, events []pg.SiteLifecycleEvent) error
}

type heldNameSource interface {
	IsHeld(ctx context.Context, slug sitekey.Slug) (bool, error)
}

type expiredClaimSource interface {
	IsExpiredReservation(ctx context.Context, slug sitekey.Slug) (bool, error)
}

type reservationWiring interface {
	reclaimableSource
	heldNameSource
	expiredClaimSource
	reclaimClaimer
	auditedReleaser
}

func scheduleReclaims(ctx context.Context, src reclaimableSource, emit lifecycleEmitter,
	dirname func(sitekey.Slug) sitekey.Dirname, now func() time.Time, dryRun bool,
) (int, error) {
	if src == nil || emit == nil {
		slog.WarnContext(ctx, "reservation.sweep.unwired",
			"source", src != nil, "emitter", emit != nil)
		return 0, nil
	}
	rows, err := src.ReclaimableReservations(ctx, now().UTC(), reclaimClaimTTL, reservationSweepLimit)
	if err != nil {
		return 0, fmt.Errorf("reservation sweep: %w", err)
	}
	if dryRun {
		for _, r := range rows {
			slog.InfoContext(ctx, "reservation.sweep.would_emit",
				"slug", r.Slug, "reservedUntil", r.ReservedUntil)
		}
		warnIfSweepCapped(ctx, len(rows))
		return 0, nil
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if dirname == nil {
		return 0, errors.New("reservation sweep: live run without a dirname template (wiring bug)")
	}
	events := make([]pg.SiteLifecycleEvent, 0, len(rows))
	for _, r := range rows {
		events = append(events, pg.SiteLifecycleEvent{
			Action: pg.LifecycleActionReclaim,
			Slug:   string(r.Slug),
			Site:   string(dirname(r.Slug)),
		})
	}
	if err := emit.EnqueueSiteLifecycle(ctx, events); err != nil {
		return 0, fmt.Errorf("reservation sweep emit: %w", err)
	}
	for _, r := range rows {
		slog.InfoContext(ctx, "reservation.sweep.emitted",
			"slug", r.Slug, "reservedUntil", r.ReservedUntil)
	}
	warnIfSweepCapped(ctx, len(rows))
	return len(rows), nil
}

func warnIfSweepCapped(ctx context.Context, n int) {
	if n == reservationSweepLimit {
		slog.WarnContext(ctx, "reservation.sweep.capped",
			"limit", reservationSweepLimit,
			"detail", "more names were expired than one run emits; the rest wait for tomorrow")
	}
}

func runReservationSweep(ctx context.Context, src reclaimableSource, emit lifecycleEmitter,
	dirname func(sitekey.Slug) sitekey.Dirname, now func() time.Time, dryRun bool,
) error {
	n, err := scheduleReclaims(ctx, src, emit, dirname, now, dryRun)
	if n > 0 {
		slog.InfoContext(ctx, "reservation.sweep.done", "emitted", n)
	}
	if err != nil {
		observability.CaptureBackground(opReservationSweep, err)
		return err
	}
	return nil
}
