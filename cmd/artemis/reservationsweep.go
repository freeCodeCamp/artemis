package main

import (
	"context"
	"errors"
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
	ReleaseReservation(ctx context.Context, slug sitekey.Slug) error
}

// siteReclaimer moves a released site's remaining bytes off the origin
// prefix. tombstone-purge collects _trash only, so a site whose name is
// freed without this leaves its objects at the origin with no collector
// and no alert.
type siteReclaimer interface {
	MovePrefix(ctx context.Context, srcPrefix, dstPrefix string) (int, error)
}

type sitePurgeRecorder interface {
	RecordSitePurge(ctx context.Context, site sitekey.Dirname) error
}

type reclaimDeps struct {
	Mover     siteReclaimer
	Tombstone sitePurgeRecorder
	Dirname   func(sitekey.Slug) sitekey.Dirname
	TrashBase string
}

func sweepExpiredReservations(ctx context.Context, src expiredReservationSource,
	rel reservationReleaser, deps reclaimDeps, now func() time.Time, dryRun bool) (int, error) {
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
		if err := reclaimSiteBytes(ctx, deps, res.Slug); err != nil {
			return released, err
		}
		if err := rel.ReleaseReservation(ctx, res.Slug); err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				slog.WarnContext(ctx, "reservation.sweep.claim_lost",
					"slug", res.Slug,
					"detail", "the row stopped being an expired reservation after its bytes were trashed")
				continue
			}
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
	rel reservationReleaser, deps reclaimDeps, now func() time.Time, dryRun bool) error {
	n, err := sweepExpiredReservations(ctx, src, rel, deps, now, dryRun)
	if err != nil {
		observability.CaptureBackground(opReservationSweep, err)
		return err
	}
	if n > 0 {
		slog.InfoContext(ctx, "reservation.sweep.done", "released", n)
	}
	return nil
}

// reclaimSiteBytes moves what remains at the origin prefix into trash and
// records the tombstone that makes tombstone-purge responsible for it. It
// runs only after the reservation has expired, and RegistryStore.Undelete
// refuses a reservation past its deadline, so no writer can return the row
// to service between the snapshot and this move — which is the guard that
// makes an origin-prefix move safe at all.
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
