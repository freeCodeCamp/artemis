package gc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type ReconcileLister interface {
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}

type ReconcileStore interface {
	DeploysForSite(ctx context.Context, site string) ([]Deploy, error)
	AliasTargets(ctx context.Context, site string) (map[string]struct{}, time.Time, error)
	UpsertDeploy(ctx context.Context, site, id string, mtime time.Time, bytes int64, hasMarker bool, state string) error
	RecordTombstone(ctx context.Context, site, id string, bytes int64) error
	PruneDeploy(ctx context.Context, site, id string) error
}

type Reconciler struct {
	Lister       ReconcileLister
	Store        ReconcileStore
	Mover        Mover
	Grace        time.Duration
	SitePrefix   func(site string) string
	DeployPrefix func(site, id string) string
	TrashPrefix  func(site, id string) string
	Now          func() time.Time
	Metrics      *Metrics
}

type DriftReport struct {
	Site             string
	Reindexed        []string
	OrphanTombstoned []string
	PGPruned         []string
	AliasedMissing   []string
}

type r2Deploy struct {
	hasMarker bool
	mtime     time.Time
}

func (rc *Reconciler) ReconcileSite(ctx context.Context, site string) (DriftReport, error) {
	report := DriftReport{Site: site}

	keys, err := rc.Lister.ListPrefix(ctx, rc.SitePrefix(site))
	if err != nil {
		return report, fmt.Errorf("reconcile %s: list r2: %w", site, err)
	}
	sitePrefix := rc.SitePrefix(site)
	r2 := map[string]*r2Deploy{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, sitePrefix)
		id := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			id = rest[:i]
		}
		if id == "" {
			continue
		}
		d, ok := r2[id]
		if !ok {
			d = &r2Deploy{mtime: parseDeployTime(id, rc.Now())}
			r2[id] = d
		}
		if rest == id+"/"+MarkerObjectName {
			d.hasMarker = true
		}
	}

	pgDeploys, err := rc.Store.DeploysForSite(ctx, site)
	if err != nil {
		return report, fmt.Errorf("reconcile %s: load pg: %w", site, err)
	}
	pg := map[string]struct{}{}
	for _, d := range pgDeploys {
		pg[d.ID] = struct{}{}
	}
	aliases, _, err := rc.Store.AliasTargets(ctx, site)
	if err != nil {
		return report, fmt.Errorf("reconcile %s: load aliases: %w", site, err)
	}

	for id, info := range r2 {
		if _, indexed := pg[id]; indexed {
			continue
		}
		if _, aliased := aliases[id]; aliased {
			report.AliasedMissing = append(report.AliasedMissing, id)
			slog.WarnContext(ctx, "reconcile.aliased_unindexed", "site", site, "deploy_id", id,
				"detail", "alias targets a deploy with no PG row; reindex, never tombstone (V1)")
			if info.hasMarker {
				if err := rc.Store.UpsertDeploy(ctx, site, id, info.mtime, 0, true, "active"); err != nil {
					return report, fmt.Errorf("reconcile %s: reindex aliased %s: %w", site, id, err)
				}
				report.Reindexed = append(report.Reindexed, id)
			}
			continue
		}
		switch {
		case info.hasMarker:
			if err := rc.Store.UpsertDeploy(ctx, site, id, info.mtime, 0, true, "active"); err != nil {
				return report, fmt.Errorf("reconcile %s: reindex %s: %w", site, id, err)
			}
			report.Reindexed = append(report.Reindexed, id)
		case rc.Now().Sub(info.mtime) >= rc.Grace:
			nowAliases, _, err := rc.Store.AliasTargets(ctx, site)
			if err != nil {
				return report, fmt.Errorf("reconcile %s: re-read aliases before tombstone %s: %w", site, id, err)
			}
			if _, nowAliased := nowAliases[id]; nowAliased {
				report.AliasedMissing = append(report.AliasedMissing, id)
				slog.WarnContext(ctx, "reconcile.aliased_raced", "site", site, "deploy_id", id,
					"detail", "alias appeared after snapshot read; skip tombstone (V1)")
				continue
			}
			if _, err := rc.Mover.MovePrefix(ctx, rc.DeployPrefix(site, id), rc.TrashPrefix(site, id)); err != nil {
				return report, fmt.Errorf("reconcile %s: tombstone orphan %s: %w", site, id, err)
			}
			if err := rc.Store.RecordTombstone(ctx, site, id, 0); err != nil {
				return report, fmt.Errorf("reconcile %s: record orphan %s: %w", site, id, err)
			}
			report.OrphanTombstoned = append(report.OrphanTombstoned, id)
		}
	}

	for id := range pg {
		if _, present := r2[id]; present {
			continue
		}
		if _, aliased := aliases[id]; aliased {
			report.AliasedMissing = append(report.AliasedMissing, id)
			slog.WarnContext(ctx, "reconcile.aliased_bytes_missing", "site", site, "deploy_id", id,
				"detail", "alias targets a deploy whose R2 bytes are gone")
			continue
		}
		if err := rc.Store.PruneDeploy(ctx, site, id); err != nil {
			return report, fmt.Errorf("reconcile %s: prune %s: %w", site, id, err)
		}
		report.PGPruned = append(report.PGPruned, id)
	}

	rc.Metrics.drift("reindexed", len(report.Reindexed))
	rc.Metrics.drift("orphan", len(report.OrphanTombstoned))
	rc.Metrics.drift("pruned", len(report.PGPruned))
	rc.Metrics.drift("aliased_missing", len(report.AliasedMissing))
	slog.InfoContext(ctx, "reconcile.site.done", "site", site,
		"reindexed", len(report.Reindexed),
		"orphanTombstoned", len(report.OrphanTombstoned),
		"pgPruned", len(report.PGPruned),
		"aliasedMissing", len(report.AliasedMissing))
	return report, nil
}

func parseDeployTime(id string, fallback time.Time) time.Time {
	if len(id) >= 15 {
		if t, err := time.Parse("20060102-150405", id[:15]); err == nil {
			return t.UTC()
		}
	}
	return fallback
}
