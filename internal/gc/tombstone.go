package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type Tombstone struct {
	Site      sitekey.Dirname
	ID        string
	TrashedAt time.Time
	Bytes     int64
}

type TombstoneReaper interface {
	ExpiredTombstones(ctx context.Context, before time.Time) ([]Tombstone, error)
	ClearTombstone(ctx context.Context, site sitekey.Dirname, id string) (bool, error)
	TombstonesForSite(ctx context.Context, site sitekey.Dirname) ([]Tombstone, error)
}

type Deleter interface {
	DeletePrefix(ctx context.Context, prefix string) (int, error)
}

type SiteLocker interface {
	WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error
}

type PurgeAuditor interface {
	RecordPurge(ctx context.Context, site sitekey.Dirname, deployID string) error
}

type TombstonePurge struct {
	Store     TombstoneReaper
	Deleter   Deleter
	Recovery  time.Duration
	TrashBase string
	Now       func() time.Time
	Locker    SiteLocker
	Audit     PurgeAuditor
	BlastCap  int
}

func (p *TombstonePurge) withLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error {
	if p.Locker == nil {
		return fn(ctx)
	}
	return p.Locker.WithSiteLock(ctx, site, fn)
}

type PurgeResult struct {
	Purged         []string
	BytesReclaimed int64
	DryRun         bool
}

func (p *TombstonePurge) trashPrefix(t Tombstone) string {
	base := p.TrashBase
	if base == "" {
		base = "_trash/"
	}
	if t.ID == "" {
		return base + string(t.Site) + "/"
	}
	return base + string(t.Site) + "/" + t.ID + "/"
}

func (p *TombstonePurge) Run(ctx context.Context, dryRun bool) (PurgeResult, error) {
	res := PurgeResult{DryRun: dryRun}
	cutoff := p.Now().Add(-p.Recovery)
	expired, err := p.Store.ExpiredTombstones(ctx, cutoff)
	if err != nil {
		return res, fmt.Errorf("tombstone-purge: list expired: %w", err)
	}
	if !dryRun && len(expired) > 0 {
		if p.BlastCap <= 0 {
			slog.WarnContext(ctx, "gc.tombstone-purge.capped", "expired", len(expired),
				"reason", "refusing every hard delete: blast-cap 0 means no ceiling was configured")
			expired = nil
		} else if len(expired) > p.BlastCap {
			slog.WarnContext(ctx, "gc.tombstone-purge.capped", "expired", len(expired), "cap", p.BlastCap,
				"reason", "hard-deleting the most overdue trash first; the remainder waits for the next run")
			expired = capOldest(expired, p.BlastCap, olderTombstone)
		}
	}
	var runErrs []error
	done := map[string]struct{}{}
	for _, t := range expired {
		label := string(t.Site) + "/" + t.ID
		if _, seen := done[label]; seen {
			continue
		}
		if dryRun {
			res.Purged = append(res.Purged, label)
			continue
		}
		var cleared bool
		covered := []Tombstone{t}
		lockErr := p.withLock(ctx, t.Site, func(lockCtx context.Context) error {
			if t.ID == "" {
				all, err := p.Store.TombstonesForSite(lockCtx, t.Site)
				if err != nil {
					return fmt.Errorf("tombstone-purge: list %s: %w", label, err)
				}
				covered = all
			}
			if _, err := p.Deleter.DeletePrefix(lockCtx, p.trashPrefix(t)); err != nil {
				return fmt.Errorf("tombstone-purge: delete %s: %w", label, err)
			}
			c, err := p.Store.ClearTombstone(lockCtx, t.Site, t.ID)
			if err != nil {
				return fmt.Errorf("tombstone-purge: clear %s: %w", label, err)
			}
			cleared = c
			return nil
		})
		if lockErr != nil {
			slog.WarnContext(ctx, "gc.tombstone-purge.site_failed", "site", t.Site, "deploy_id", t.ID, "err", lockErr)
			runErrs = append(runErrs, lockErr)
			continue
		}
		if !cleared {
			continue
		}
		for _, c := range covered {
			done[string(c.Site)+"/"+c.ID] = struct{}{}
			res.Purged = append(res.Purged, string(c.Site)+"/"+c.ID)
			res.BytesReclaimed += c.Bytes
			if p.Audit != nil {
				if err := p.Audit.RecordPurge(ctx, c.Site, c.ID); err != nil {
					slog.ErrorContext(ctx, "gc.tombstone-purge.audit_failed", "site", c.Site, "deploy_id", c.ID, "err", err)
				}
			}
		}
	}

	slog.InfoContext(ctx, "gc.tombstone-purge.done", "purged", len(res.Purged), "bytes", res.BytesReclaimed, "dryRun", dryRun)
	return res, errors.Join(runErrs...)
}

func olderTombstone(a, b Tombstone) bool {
	if !a.TrashedAt.Equal(b.TrashedAt) {
		return a.TrashedAt.Before(b.TrashedAt)
	}
	if a.Site != b.Site {
		return a.Site < b.Site
	}
	return a.ID < b.ID
}
