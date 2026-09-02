package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	opSiteReclaim      = "site.reclaim"
	reclaimActor       = "system:gc"
	reclaimClaimTTL    = 12 * time.Hour
	reclaimParallelism = 4
)

type siteReclaimer interface {
	MovePrefix(ctx context.Context, srcPrefix, dstPrefix string) (int, error)
}

type siteTombstoneRecorder interface {
	RecordSiteTombstone(ctx context.Context, site sitekey.Dirname) error
}

type reclaimClaimer interface {
	ClaimReclaim(ctx context.Context, slug sitekey.Slug, claimTTL time.Duration) (bool, error)
}

type auditedReleaser interface {
	ReleaseReservationAudited(ctx context.Context, slug sitekey.Slug, e pg.AuditEvent) error
}

type reclaimDeps struct {
	Mover     siteReclaimer
	Tombstone siteTombstoneRecorder
	Locker    gc.Locker
	Expired   func(ctx context.Context, slug sitekey.Slug) (bool, error)
	Claimer   reclaimClaimer
	Releaser  auditedReleaser
	Dirname   func(sitekey.Slug) sitekey.Dirname
	TrashBase string
}

type lifecycleInput struct {
	Action string
	Slug   sitekey.Slug
	Site   sitekey.Dirname
}

func lifecycleInputFrom(input map[string]any) (lifecycleInput, error) {
	action, _ := input["action"].(string)
	slug, _ := input["slug"].(string)
	site, _ := input["site"].(string)
	if action == "" || slug == "" {
		return lifecycleInput{}, errors.New("workflow input missing action or slug")
	}
	return lifecycleInput{Action: action, Slug: sitekey.Slug(slug), Site: sitekey.Dirname(site)}, nil
}

func runSiteReclaim(ctx context.Context, deps reclaimDeps, input map[string]any, dryRun bool) error {
	in, err := lifecycleInputFrom(input)
	if err != nil {
		return err
	}
	if in.Action != pg.LifecycleActionReclaim {
		return fmt.Errorf("site.lifecycle: unknown action %q for %s", in.Action, in.Slug)
	}
	if deps.Dirname == nil || deps.Expired == nil {
		return errors.New("site.reclaim: run without a dirname template or expiry check (wiring bug)")
	}
	dirname := deps.Dirname(in.Slug)
	if in.Site != "" && in.Site != dirname {
		return fmt.Errorf("site.reclaim %s: payload site %q is not the slug's dirname %q", in.Slug, in.Site, dirname)
	}
	if dryRun {
		expired, err := deps.Expired(ctx, in.Slug)
		if err != nil {
			return fmt.Errorf("site.reclaim expiry check %s: %w", in.Slug, err)
		}
		slog.InfoContext(ctx, "site.reclaim.would_reclaim", "slug", in.Slug, "site", dirname, "expired", expired)
		return nil
	}
	if deps.Locker == nil || deps.Claimer == nil || deps.Releaser == nil || deps.Mover == nil || deps.Tombstone == nil {
		return errors.New("site.reclaim: live run without lock, claim, mover, tombstone or audited release (wiring bug)")
	}
	won, err := deps.Claimer.ClaimReclaim(ctx, in.Slug, reclaimClaimTTL)
	if err != nil {
		return fmt.Errorf("site.reclaim claim %s: %w", in.Slug, err)
	}
	if !won {
		slog.InfoContext(ctx, "site.reclaim.claim.not_ours", "slug", in.Slug,
			"detail", "another run holds the claim inside its TTL, or the row is already released; a duplicate or late event is a no-op")
		return nil
	}
	sess, err := deps.Locker.NewLockSession(ctx)
	if err != nil {
		return fmt.Errorf("site.reclaim lock session %s: %w", in.Slug, err)
	}
	defer sess.Close(ctx)
	return sess.WithSiteLock(ctx, dirname, func(ctx context.Context) error {
		expired, err := deps.Expired(ctx, in.Slug)
		if err != nil {
			return fmt.Errorf("site.reclaim expiry check %s: %w", in.Slug, err)
		}
		if !expired {
			slog.WarnContext(ctx, "site.reclaim.claim_not_held", "slug", in.Slug, "site", dirname,
				"detail", "under the lock the row is not an expired reservation; its bytes belong to someone else")
			return nil
		}
		moved, err := reclaimSiteBytes(ctx, deps, dirname)
		if err != nil {
			return err
		}
		err = deps.Releaser.ReleaseReservationAudited(ctx, in.Slug, pg.AuditEvent{
			Actor:   reclaimActor,
			Action:  opSiteReclaim,
			Site:    string(dirname),
			Outcome: "success",
			Detail:  map[string]any{"moved": moved, "tombstoned": true},
		})
		if errors.Is(err, registry.ErrNotFound) {
			slog.WarnContext(ctx, "site.reclaim.claim_lost", "slug", in.Slug, "site", dirname,
				"detail", "the row stopped being an expired reservation after its bytes were trashed; the tombstone lets the purge collect them")
			return nil
		}
		if err != nil {
			return fmt.Errorf("site.reclaim release %s: %w", in.Slug, err)
		}
		slog.InfoContext(ctx, "site.reclaim.released", "slug", in.Slug, "site", dirname, "moved", moved)
		return nil
	})
}

func reclaimSiteBytes(ctx context.Context, deps reclaimDeps, dirname sitekey.Dirname) (int, error) {
	base := deps.TrashBase
	if base == "" {
		base = "_trash/"
	}
	if err := deps.Tombstone.RecordSiteTombstone(ctx, dirname); err != nil {
		return 0, fmt.Errorf("site.reclaim tombstone %s: %w", dirname, err)
	}
	src := string(dirname) + "/"
	n, err := deps.Mover.MovePrefix(ctx, src, base+src)
	if err != nil {
		return 0, fmt.Errorf("site.reclaim move %s: %w", dirname, err)
	}
	return n, nil
}
