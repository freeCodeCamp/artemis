package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opReservationSweep    = "reservation.sweep"
	reservationSweepLimit = 50
)

type expiredReservationSource interface {
	ExpiredReservations(ctx context.Context, before time.Time, limit int) ([]registry.Reservation, error)
}

type reservationReleaser interface {
	Delete(ctx context.Context, slug sitekey.Slug) error
}

func sweepExpiredReservations(ctx context.Context, src expiredReservationSource,
	rel reservationReleaser, now func() time.Time, dryRun bool) (int, error) {
	if src == nil || rel == nil {
		return 0, nil
	}
	expired, err := src.ExpiredReservations(ctx, now().UTC(), reservationSweepLimit)
	if err != nil {
		return 0, fmt.Errorf("reservation sweep: %w", err)
	}
	released := 0
	for _, res := range expired {
		if dryRun {
			slog.InfoContext(ctx, "reservation.sweep.would_release",
				"slug", res.Slug, "reservedUntil", res.ReservedUntil)
			continue
		}
		if err := rel.Delete(ctx, res.Slug); err != nil {
			return released, fmt.Errorf("reservation sweep release %s: %w", res.Slug, err)
		}
		released++
		slog.InfoContext(ctx, "reservation.sweep.released",
			"slug", res.Slug, "reservedUntil", res.ReservedUntil)
	}
	if len(expired) == reservationSweepLimit {
		slog.WarnContext(ctx, "reservation.sweep.capped",
			"limit", reservationSweepLimit,
			"detail", "more names were expired than one run releases; the rest wait for tomorrow")
	}
	return released, nil
}

func runReservationSweep(ctx context.Context, src expiredReservationSource,
	rel reservationReleaser, now func() time.Time, dryRun bool) error {
	n, err := sweepExpiredReservations(ctx, src, rel, now, dryRun)
	if err != nil {
		observability.CaptureBackground(opReservationSweep, err)
		return err
	}
	if n > 0 {
		slog.InfoContext(ctx, "reservation.sweep.done", "released", n)
	}
	return nil
}
