package gc

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
)

const reconcileOpTimeout = 10 * time.Minute

type ReconcileLister interface {
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}

type ReconcileStore interface {
	DeploysForSite(ctx context.Context, site string) ([]Deploy, error)
	AliasTargets(ctx context.Context, site string) (map[string]struct{}, time.Time, error)
	ReindexDeploy(ctx context.Context, site, id string, mtime time.Time, hasMarker bool) (bool, error)
	RecordTombstone(ctx context.Context, site, id string, bytes int64) error
	PruneDeploy(ctx context.Context, site, id string) error
}

type Reconciler struct {
	Lister       ReconcileLister
	Locker       Locker
	Store        ReconcileStore
	Mover        Mover
	Grace        time.Duration
	BlastCap     int
	SitePrefix   func(site string) string
	DeployPrefix func(site, id string) string
	TrashPrefix  func(site, id string) string
	LiveAliases  func(ctx context.Context, site string) (map[string]struct{}, error)
	Now          func() time.Time
	Audit        GCAuditor
	PruneAudit   GCAuditor
}

type DriftReport struct {
	Site             string
	Reindexed        []string
	OrphanTombstoned []string
	PGPruned         []string
	AliasedMissing   []string
	Capped           bool
	CapReason        string
}

const orphanBytesUnknown int64 = 0

type r2Deploy struct {
	hasMarker bool
	mtime     time.Time
}

type siteSnapshot struct {
	r2      map[string]*r2Deploy
	indexed map[string]Deploy
	aliases map[string]struct{}
}

type repairPlan struct {
	reindex   []string
	tombstone []string
	prune     []string
}

func (rc *Reconciler) ReconcileSite(ctx context.Context, site string, dryRun bool) (DriftReport, error) {
	report := DriftReport{Site: site}

	snap, err := rc.snapshot(ctx, site)
	if err != nil {
		return report, fmt.Errorf("reconcile %s: %w", site, err)
	}
	if len(snap.r2) == 0 && len(snap.indexed) > 0 {
		return report, fmt.Errorf(
			"reconcile %s: postgres holds %d active deploys but the listing under %q returned no deploy prefix: "+
				"refusing to treat an empty listing as total drift",
			site, len(snap.indexed), rc.SitePrefix(site))
	}
	plan := rc.classify(ctx, site, snap, &report)

	if dryRun {
		report.Reindexed = plan.reindex
		report.OrphanTombstoned = plan.tombstone
		report.PGPruned = plan.prune
		rc.predictBlastCap(ctx, plan, &report)
		rc.logDone(ctx, report, true)
		return report, nil
	}
	rc.applyBlastCap(ctx, site, &plan, &report)

	if rc.Locker == nil {
		return report, fmt.Errorf("reconcile %s: live run without site Locker (wiring bug)", site)
	}
	if rc.LiveAliases == nil {
		return report, fmt.Errorf("reconcile %s: live run without LiveAliases reader (wiring bug)", site)
	}
	sessCtx, sessCancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileOpTimeout)
	sess, err := rc.Locker.NewLockSession(sessCtx)
	sessCancel()
	if err != nil {
		return report, fmt.Errorf("reconcile %s: open lock session: %w", site, err)
	}
	defer sess.Close(ctx)

	repairErr := rc.repair(ctx, sess, site, snap, plan, &report)
	rc.logDone(ctx, report, false)
	return report, repairErr
}

func (rc *Reconciler) snapshot(ctx context.Context, site string) (siteSnapshot, error) {
	sitePrefix := rc.SitePrefix(site)
	keys, err := rc.Lister.ListPrefix(ctx, sitePrefix)
	if err != nil {
		return siteSnapshot{}, fmt.Errorf("list r2: %w", err)
	}

	snap := siteSnapshot{r2: map[string]*r2Deploy{}, indexed: map[string]Deploy{}}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, sitePrefix)
		id := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			id = rest[:i]
		}
		if id == "" {
			continue
		}
		d, ok := snap.r2[id]
		if !ok {
			d = &r2Deploy{mtime: parseDeployTime(id, rc.Now())}
			snap.r2[id] = d
		}
		if rest == id+"/"+MarkerObjectName {
			d.hasMarker = true
		}
	}

	pgDeploys, err := rc.Store.DeploysForSite(ctx, site)
	if err != nil {
		return siteSnapshot{}, fmt.Errorf("load pg: %w", err)
	}
	for _, d := range pgDeploys {
		snap.indexed[d.ID] = d
	}

	aliases, _, err := rc.Store.AliasTargets(ctx, site)
	if err != nil {
		return siteSnapshot{}, fmt.Errorf("load aliases: %w", err)
	}
	snap.aliases = maps.Clone(aliases)
	if snap.aliases == nil {
		snap.aliases = map[string]struct{}{}
	}
	if rc.LiveAliases != nil {
		live, err := rc.LiveAliases(ctx, site)
		if err != nil {
			return siteSnapshot{}, fmt.Errorf("load live r2 aliases: %w", err)
		}
		maps.Copy(snap.aliases, live)
	}
	return snap, nil
}

func (rc *Reconciler) classify(ctx context.Context, site string, snap siteSnapshot, report *DriftReport) repairPlan {
	var plan repairPlan

	for _, id := range slices.Sorted(maps.Keys(snap.r2)) {
		info := snap.r2[id]
		if _, indexed := snap.indexed[id]; indexed {
			continue
		}
		_, aliased := snap.aliases[id]
		switch {
		case info.hasMarker:
			plan.reindex = append(plan.reindex, id)
			if aliased {
				slog.WarnContext(ctx, "reconcile.aliased_unindexed_reindexed", "site", site, "deploy_id", id,
					"detail", "alias targeted an unindexed deploy with a marker; self-healed via reindex, no page")
			}
		case aliased:
			report.AliasedMissing = append(report.AliasedMissing, id)
			slog.WarnContext(ctx, "reconcile.aliased_unindexed", "site", site, "deploy_id", id,
				"detail", "alias targets a deploy with no PG row and no marker; reindex, never tombstone (V1)")
		case rc.Now().Sub(info.mtime) >= rc.Grace:
			plan.tombstone = append(plan.tombstone, id)
		}
	}

	for _, id := range slices.Sorted(maps.Keys(snap.indexed)) {
		if _, present := snap.r2[id]; present {
			continue
		}
		if _, aliased := snap.aliases[id]; aliased {
			report.AliasedMissing = append(report.AliasedMissing, id)
			slog.WarnContext(ctx, "reconcile.aliased_bytes_missing", "site", site, "deploy_id", id,
				"detail", "alias targets a deploy whose R2 bytes are gone")
			continue
		}
		if rc.Now().Sub(snap.indexed[id].Mtime) < rc.Grace {
			slog.InfoContext(ctx, "reconcile.prune_deferred", "site", site, "deploy_id", id,
				"detail", "PG row is younger than grace; its bytes may still be uploading")
			continue
		}
		plan.prune = append(plan.prune, id)
	}
	return plan
}

func (rc *Reconciler) predictBlastCap(ctx context.Context, plan repairPlan, report *DriftReport) {
	capped := plan
	rc.applyBlastCap(ctx, report.Site, &capped, report)
}

func (rc *Reconciler) applyBlastCap(ctx context.Context, site string, plan *repairPlan, report *DriftReport) {
	destructive := len(plan.tombstone) + len(plan.prune)
	if destructive == 0 || (rc.BlastCap > 0 && destructive <= rc.BlastCap) {
		return
	}
	if rc.BlastCap <= 0 {
		report.Capped = true
		report.CapReason = fmt.Sprintf(
			"refusing %d destructive repairs: blast-cap 0 means no ceiling was configured", destructive)
		plan.tombstone = nil
		plan.prune = nil
		slog.WarnContext(ctx, "reconcile.capped", "site", site, "reason", report.CapReason)
		return
	}
	report.Capped = true
	report.CapReason = fmt.Sprintf("destructive plan of %d exceeds blast-cap %d; attempting %d this run",
		destructive, rc.BlastCap, rc.BlastCap)
	if len(plan.tombstone) >= rc.BlastCap {
		plan.tombstone = plan.tombstone[:rc.BlastCap]
		plan.prune = nil
	} else {
		plan.prune = plan.prune[:rc.BlastCap-len(plan.tombstone)]
	}
	slog.WarnContext(ctx, "reconcile.capped", "site", site, "reason", report.CapReason)
}

func (rc *Reconciler) repair(ctx context.Context, sess LockSession, site string, snap siteSnapshot, plan repairPlan, report *DriftReport) error {
	for _, id := range plan.reindex {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("reconcile %s: %w", site, err)
		}
		mtime := snap.r2[id].mtime
		var bytesGone bool
		done, err := rc.locked(ctx, sess, site, func(opCtx context.Context) (bool, error) {
			keys, err := rc.Lister.ListPrefix(opCtx, rc.DeployPrefix(site, id))
			if err != nil {
				return false, fmt.Errorf("re-list r2 before reindex %s: %w", id, err)
			}
			if len(keys) == 0 {
				bytesGone = true
				return false, nil
			}
			marked := slices.Contains(keys, rc.DeployPrefix(site, id)+MarkerObjectName)
			return rc.Store.ReindexDeploy(opCtx, site, id, mtime, marked)
		})
		if done {
			report.Reindexed = append(report.Reindexed, id)
		}
		if err != nil {
			return fmt.Errorf("reconcile %s: reindex %s: %w", site, id, err)
		}
		if !done {
			slog.WarnContext(ctx, "reconcile.reindex_refused", "site", site, "deploy_id", id,
				"detail", reindexRefusalReason(bytesGone))
			if _, aliased := snap.aliases[id]; aliased {
				report.AliasedMissing = append(report.AliasedMissing, id)
			}
		}
	}

	for _, id := range plan.tombstone {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("reconcile %s: %w", site, err)
		}
		var verdict tombstoneVerdict
		var rowRecorded bool
		done, err := rc.locked(ctx, sess, site, func(opCtx context.Context) (bool, error) {
			v, err := rc.recheckOrphan(opCtx, site, id)
			verdict = v
			if err != nil || v != tombstoneProceed {
				return false, err
			}
			trash := rc.TrashPrefix(site, id)
			if err := rc.Store.RecordTombstone(opCtx, site, id, orphanBytesUnknown); err != nil {
				return false, fmt.Errorf("record orphan %s: %w", id, err)
			}
			rowRecorded = true
			if _, err := rc.Mover.MovePrefix(opCtx, rc.DeployPrefix(site, id), trash); err != nil {
				slog.WarnContext(opCtx, "reconcile.tombstone_move_deferred", "site", site, "deploy_id", id,
					"trash_prefix", trash, "err", err,
					"detail", "the row landed before the move, so the bytes stay at the deploy prefix and the next run retries them")
				return false, fmt.Errorf("tombstone orphan %s: %w", id, err)
			}
			return true, nil
		})
		if rowRecorded {
			rc.audit(ctx, site, id)
		}
		if done {
			report.OrphanTombstoned = append(report.OrphanTombstoned, id)
		}
		if err != nil {
			return fmt.Errorf("reconcile %s: %w", site, err)
		}
		if !done {
			slog.WarnContext(ctx, "reconcile.tombstone_skipped", "site", site, "deploy_id", id,
				"detail", verdict.reason())
			if verdict == tombstoneSkipAliased {
				report.AliasedMissing = append(report.AliasedMissing, id)
			}
		}
	}

	for _, id := range plan.prune {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("reconcile %s: %w", site, err)
		}
		var verdict pruneVerdict
		done, err := rc.locked(ctx, sess, site, func(opCtx context.Context) (bool, error) {
			v, err := rc.recheckGhost(opCtx, site, id)
			verdict = v
			if err != nil || v != pruneProceed {
				return false, err
			}
			if err := rc.Store.PruneDeploy(opCtx, site, id); err != nil {
				return false, err
			}
			return true, nil
		})
		if done {
			report.PGPruned = append(report.PGPruned, id)
			slog.WarnContext(ctx, "reconcile.pruned", "site", site, "deploy_id", id,
				"mtime", snap.indexed[id].Mtime,
				"detail", "index row deleted; R2 held no bytes for it under the lock")
			rc.auditWith(ctx, rc.PruneAudit, site, id)
		}
		if err != nil {
			return fmt.Errorf("reconcile %s: prune %s: %w", site, id, err)
		}
		if !done {
			slog.WarnContext(ctx, "reconcile.prune_skipped", "site", site, "deploy_id", id,
				"detail", verdict.reason())
			if verdict == pruneSkipAliased {
				report.AliasedMissing = append(report.AliasedMissing, id)
			}
		}
	}
	return nil
}

func reindexRefusalReason(bytesGone bool) string {
	if bytesGone {
		return "bytes vanished between the snapshot and the lock; nothing left to index"
	}
	return "deploy is tombstoned; the snapshot predates the trash move, refusing to resurrect it"
}

func (rc *Reconciler) locked(ctx context.Context, sess LockSession, site string, fn func(context.Context) (bool, error)) (bool, error) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileOpTimeout)
	defer cancel()

	var done bool
	err := sess.WithSiteLock(opCtx, site, func() error {
		var fnErr error
		done, fnErr = fn(opCtx)
		return fnErr
	})
	return done, err
}

type tombstoneVerdict int

const (
	tombstoneProceed tombstoneVerdict = iota
	tombstoneSkipIndexed
	tombstoneSkipAliased
	tombstoneSkipGone
	tombstoneSkipFinalized
)

func (v tombstoneVerdict) reason() string {
	switch v {
	case tombstoneSkipIndexed:
		return "deploy gained a PG row after the snapshot; it is live, skip tombstone"
	case tombstoneSkipAliased:
		return "alias appeared after the snapshot; skip tombstone (V1)"
	case tombstoneSkipGone:
		return "bytes are already gone from R2; nothing left to tombstone"
	case tombstoneSkipFinalized:
		return "marker appeared after the snapshot; the deploy finalized, skip tombstone"
	}
	return "proceed"
}

func (rc *Reconciler) aliasedNow(ctx context.Context, site, id string) (bool, error) {
	aliases, _, err := rc.Store.AliasTargets(ctx, site)
	if err != nil {
		return false, err
	}
	if _, aliased := aliases[id]; aliased {
		return true, nil
	}
	live, err := rc.LiveAliases(ctx, site)
	if err != nil {
		return false, fmt.Errorf("live r2 aliases: %w", err)
	}
	_, aliased := live[id]
	return aliased, nil
}

func (rc *Reconciler) recheckOrphan(ctx context.Context, site, id string) (tombstoneVerdict, error) {
	deploys, err := rc.Store.DeploysForSite(ctx, site)
	if err != nil {
		return tombstoneProceed, fmt.Errorf("re-read deploys before tombstone %s: %w", id, err)
	}
	for _, d := range deploys {
		if d.ID == id {
			return tombstoneSkipIndexed, nil
		}
	}
	aliased, err := rc.aliasedNow(ctx, site, id)
	if err != nil {
		return tombstoneProceed, fmt.Errorf("re-read aliases before tombstone %s: %w", id, err)
	}
	if aliased {
		return tombstoneSkipAliased, nil
	}
	deployPrefix := rc.DeployPrefix(site, id)
	keys, err := rc.Lister.ListPrefix(ctx, deployPrefix)
	if err != nil {
		return tombstoneProceed, fmt.Errorf("re-list r2 before tombstone %s: %w", id, err)
	}
	if len(keys) == 0 {
		return tombstoneSkipGone, nil
	}
	if slices.Contains(keys, deployPrefix+MarkerObjectName) {
		return tombstoneSkipFinalized, nil
	}
	return tombstoneProceed, nil
}

type pruneVerdict int

const (
	pruneProceed pruneVerdict = iota
	pruneSkipBytesPresent
	pruneSkipAliased
)

func (v pruneVerdict) reason() string {
	switch v {
	case pruneSkipBytesPresent:
		return "bytes are present in R2; the snapshot was stale, skip prune"
	case pruneSkipAliased:
		return "alias appeared after the snapshot; skip prune"
	}
	return "proceed"
}

func (rc *Reconciler) recheckGhost(ctx context.Context, site, id string) (pruneVerdict, error) {
	keys, err := rc.Lister.ListPrefix(ctx, rc.DeployPrefix(site, id))
	if err != nil {
		return pruneProceed, fmt.Errorf("re-list r2 before prune %s: %w", id, err)
	}
	if len(keys) > 0 {
		return pruneSkipBytesPresent, nil
	}
	aliased, err := rc.aliasedNow(ctx, site, id)
	if err != nil {
		return pruneProceed, fmt.Errorf("re-read aliases before prune %s: %w", id, err)
	}
	if aliased {
		return pruneSkipAliased, nil
	}
	return pruneProceed, nil
}

func (rc *Reconciler) audit(ctx context.Context, site, id string) {
	rc.auditWith(ctx, rc.Audit, site, id)
}

func (rc *Reconciler) auditWith(ctx context.Context, auditor GCAuditor, site, id string) {
	if auditor == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileOpTimeout)
	defer cancel()
	if err := auditor.AuditTombstone(auditCtx, site, id); err != nil {
		slog.WarnContext(auditCtx, "reconcile.audit_failed", "site", site, "deploy_id", id, "err", err)
	}
}

func (rc *Reconciler) logDone(ctx context.Context, report DriftReport, dryRun bool) {
	slog.InfoContext(ctx, "reconcile.site.done", "site", report.Site,
		"dryRun", dryRun,
		"reindexed", len(report.Reindexed),
		"orphanTombstoned", len(report.OrphanTombstoned),
		"pgPruned", len(report.PGPruned),
		"aliasedMissing", len(report.AliasedMissing),
		"capped", report.Capped)
}

func parseDeployTime(id string, fallback time.Time) time.Time {
	if len(id) >= 15 {
		if t, err := time.Parse("20060102-150405", id[:15]); err == nil {
			return t.UTC()
		}
	}
	return fallback
}
