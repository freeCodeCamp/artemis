package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opReservationSweep        = "reservation.sweep"
	reservationSweepLimit     = 50
	reservationReclaimTimeout = 10 * time.Minute
)

type expiredReservationSource interface {
	ExpiredReservations(ctx context.Context, before time.Time, limit int) ([]registry.Reservation, error)
}

type reservationReleaser interface {
	ReleaseReservation(ctx context.Context, slug sitekey.Slug) error
}

type reservationWiring interface {
	expiredReservationSource
	reservationReleaser
}

type siteReclaimer interface {
	MovePrefix(ctx context.Context, srcPrefix, dstPrefix string) (int, error)
}

type sitePurgeRecorder interface {
	RecordSitePurge(ctx context.Context, site sitekey.Dirname) error
}

type reclaimDeps struct {
	Mover     siteReclaimer
	Tombstone sitePurgeRecorder
	Locker    gc.Locker
	Dirname   func(sitekey.Slug) sitekey.Dirname
	TrashBase string
}

func sweepExpiredReservations(ctx context.Context, src expiredReservationSource,
	rel reservationReleaser, deps reclaimDeps, now func() time.Time, dryRun bool) (int, error) {
	if src == nil || rel == nil {
		slog.WarnContext(ctx, "reservation.sweep.unwired",
			"source", src != nil, "releaser", rel != nil)
		return 0, nil
	}
	expired, err := src.ExpiredReservations(ctx, now().UTC(), reservationSweepLimit)
	if err != nil {
		return 0, fmt.Errorf("reservation sweep: %w", err)
	}
	if dryRun {
		for _, res := range expired {
			slog.InfoContext(ctx, "reservation.sweep.would_release",
				"slug", res.Slug, "reservedUntil", res.ReservedUntil)
		}
		warnIfSweepCapped(ctx, len(expired))
		return 0, nil
	}
	if len(expired) == 0 {
		return 0, nil
	}
	if deps.Locker == nil || deps.Dirname == nil {
		return 0, errors.New("reservation sweep: live run without site Locker (wiring bug)")
	}
	sess, err := deps.Locker.NewLockSession(ctx)
	if err != nil {
		return 0, fmt.Errorf("reservation sweep: lock session: %w", err)
	}
	defer sess.Close(ctx)

	released := 0
	var runErrs []error
	for _, res := range expired {
		freed, err := releaseOneReservation(ctx, sess, rel, deps, res)
		if err != nil {
			slog.WarnContext(ctx, "reservation.sweep.site_failed",
				"slug", res.Slug, "err", err,
				"detail", "the sweep continues; a first row that always fails would otherwise block every later row forever")
			observability.CaptureBackground(opReservationSweep, err)
			runErrs = append(runErrs, err)
			continue
		}
		if !freed {
			continue
		}
		released++
		slog.InfoContext(ctx, "reservation.sweep.released",
			"slug", res.Slug, "reservedUntil", res.ReservedUntil)
	}
	warnIfSweepCapped(ctx, len(expired))
	return released, errors.Join(runErrs...)
}

func warnIfSweepCapped(ctx context.Context, n int) {
	if n == reservationSweepLimit {
		slog.WarnContext(ctx, "reservation.sweep.capped",
			"limit", reservationSweepLimit,
			"detail", "more names were expired than one run releases; the rest wait for tomorrow")
	}
}

func releaseOneReservation(ctx context.Context, sess gc.LockSession, rel reservationReleaser,
	deps reclaimDeps, res registry.Reservation) (bool, error) {
	freed := false
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reservationReclaimTimeout)
	defer cancel()
	lockErr := sess.WithSiteLock(opCtx, deps.Dirname(res.Slug), func(opCtx context.Context) error {
		if err := reclaimSiteBytes(opCtx, deps, res.Slug); err != nil {
			return err
		}
		if err := rel.ReleaseReservation(opCtx, res.Slug); err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				slog.WarnContext(opCtx, "reservation.sweep.claim_lost",
					"slug", res.Slug,
					"detail", "the row stopped being an expired reservation after its bytes were trashed")
				return nil
			}
			return fmt.Errorf("reservation sweep release %s: %w", res.Slug, err)
		}
		freed = true
		return nil
	})
	if lockErr != nil {
		return false, lockErr
	}
	return freed, nil
}

func runReservationSweep(ctx context.Context, src expiredReservationSource,
	rel reservationReleaser, deps reclaimDeps, now func() time.Time, dryRun bool) error {
	n, err := sweepExpiredReservations(ctx, src, rel, deps, now, dryRun)
	if n > 0 {
		slog.InfoContext(ctx, "reservation.sweep.done", "released", n)
	}
	if err != nil {
		if n == 0 {
			observability.CaptureBackground(opReservationSweep, err)
		}
		return err
	}
	return nil
}

func reclaimSiteBytes(ctx context.Context, deps reclaimDeps, slug sitekey.Slug) error {
	if deps.Mover == nil || deps.Dirname == nil {
		return nil
	}
	dirname := deps.Dirname(slug)
	base := deps.TrashBase
	if base == "" {
		base = "_trash/"
	}
	if deps.Tombstone != nil {
		if err := deps.Tombstone.RecordSitePurge(ctx, dirname); err != nil {
			return fmt.Errorf("reservation sweep tombstone %s: %w", dirname, err)
		}
	}
	src := string(dirname) + "/"
	n, err := deps.Mover.MovePrefix(ctx, src, base+src)
	if err != nil {
		return fmt.Errorf("reservation sweep reclaim %s: %w", dirname, err)
	}
	if n > 0 {
		slog.InfoContext(ctx, "reservation.sweep.reclaimed", "site", dirname, "moved", n)
	}
	return nil
}
