package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Tombstone struct {
	Site      string
	ID        string
	TrashedAt time.Time
	Bytes     int64
}

type TombstoneReaper interface {
	ExpiredTombstones(ctx context.Context, before time.Time) ([]Tombstone, error)
	ClearTombstone(ctx context.Context, site, id string) error
}

type Deleter interface {
	DeletePrefix(ctx context.Context, prefix string) (int, error)
}

type SiteLocker interface {
	WithSiteLock(ctx context.Context, site string, fn func() error) error
}

type TombstonePurge struct {
	Store     TombstoneReaper
	Deleter   Deleter
	Recovery  time.Duration
	TrashBase string
	Now       func() time.Time
	Metrics   *Metrics
	Locker    SiteLocker
}

func (p *TombstonePurge) withLock(ctx context.Context, site string, fn func() error) error {
	if p.Locker == nil {
		return fn()
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
		return base + t.Site + "/"
	}
	return base + t.Site + "/" + t.ID + "/"
}

func (p *TombstonePurge) Run(ctx context.Context, dryRun bool) (PurgeResult, error) {
	res := PurgeResult{DryRun: dryRun}
	cutoff := p.Now().Add(-p.Recovery)
	expired, err := p.Store.ExpiredTombstones(ctx, cutoff)
	if err != nil {
		return res, fmt.Errorf("tombstone-purge: list expired: %w", err)
	}
	for _, t := range expired {
		label := t.Site + "/" + t.ID
		if dryRun {
			res.Purged = append(res.Purged, label)
			continue
		}
		lockErr := p.withLock(ctx, t.Site, func() error {
			if _, err := p.Deleter.DeletePrefix(ctx, p.trashPrefix(t)); err != nil {
				return fmt.Errorf("tombstone-purge: delete %s: %w", label, err)
			}
			if err := p.Store.ClearTombstone(ctx, t.Site, t.ID); err != nil {
				return fmt.Errorf("tombstone-purge: clear %s: %w", label, err)
			}
			return nil
		})
		if lockErr != nil {
			return res, lockErr
		}
		res.Purged = append(res.Purged, label)
		res.BytesReclaimed += t.Bytes
	}

	if !dryRun {
		p.Metrics.reclaimed(res.BytesReclaimed)
		p.Metrics.run(WorkflowTombstonePurgeLabel, "ok")
	}
	slog.Info("gc.tombstone-purge.done", "purged", len(res.Purged), "bytes", res.BytesReclaimed, "dryRun", dryRun)
	return res, nil
}
