package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

const (
	opDriftSweep          = "drift.sweep"
	opDriftSelfCheck      = "drift.selfcheck"
	opDriftUnreadable     = "drift.unreadable"
	opDriftAliasedMissing = "drift.aliased_missing"
	opDriftOrphanAliases  = "drift.orphan_aliases"
	opDriftReclaimable    = "drift.reclaimable"
)

const reclaimableAlertThreshold = 25

type driftVerdict struct {
	Op    string
	Err   error
	Fails bool
}

func classifyDrift(res sweepResult) driftVerdict {
	unread := errors.Join(unreadableErr(unreadableSites(res.Reports), res.Stats.Sites), res.OrphanErr)
	if err := res.Stats.validate(); err != nil {
		return driftVerdict{Op: opDriftSelfCheck, Err: errors.Join(err, unread), Fails: true}
	}
	if sites, total := aliasedMissingSites(res.Reports); total > 0 {
		err := fmt.Errorf("%d deploys are aliased but missing from R2 and the index across %s: "+
			"a live site serves or will serve nothing", total, strings.Join(sites, ", "))
		return driftVerdict{
			Op:    opDriftAliasedMissing,
			Err:   errors.Join(err, unread),
			Fails: unread != nil,
		}
	}
	if unread != nil {
		return driftVerdict{Op: opDriftUnreadable, Err: unread, Fails: true}
	}
	if len(res.OrphanAliases) > 0 {
		return driftVerdict{
			Op: opDriftOrphanAliases,
			Err: fmt.Errorf(
				"%d alias key(s) serve names with no registry row (%s): a deregistered site is still on the "+
					"public internet; unpublish each with DELETE, or release it if the name is meant to go",
				len(res.OrphanAliases), strings.Join(orphanAliasNames(res.OrphanAliases), ", ")),
		}
	}
	if reindex, tombstone, _, _ := res.totals(); reindex+tombstone >= reclaimableAlertThreshold {
		sites := reclaimableSites(res.Reports)
		return driftVerdict{
			Op: opDriftReclaimable,
			Err: fmt.Errorf(
				"%d deploys are reclaimable across %s (>= %d): storage is accruing faster than it is "+
					"collected; run `artemis reconcile <site> --apply` for each and find what stopped expiring",
				reindex+tombstone, strings.Join(sites, ", "), reclaimableAlertThreshold),
		}
	}
	return driftVerdict{}
}

func unreadableErr(unreadable []string, sites int) error {
	if len(unreadable) == 0 {
		return nil
	}
	return fmt.Errorf("drift sweep could not read %d of %d sites (%s): unknown drift, not zero drift",
		len(unreadable), sites, strings.Join(unreadable, ", "))
}

func reclaimableSites(reports []siteDrift) []string {
	type sited struct {
		site string
		n    int
	}
	var ranked []sited
	for _, r := range reports {
		if n := len(r.Reindex) + len(r.Tombstone); n > 0 {
			ranked = append(ranked, sited{string(r.Site), n})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return ranked[i].site < ranked[j].site
	})
	out := make([]string, 0, len(ranked))
	for _, s := range ranked {
		out = append(out, fmt.Sprintf("%s (%d)", s.site, s.n))
	}
	return out
}

func unreadableSites(reports []siteDrift) []string {
	var out []string
	for _, r := range reports {
		if r.FailedWith != nil {
			out = append(out, string(r.Site))
		}
	}
	return out
}

func orphanAliasNames(orphans []orphanAlias) []string {
	out := make([]string, 0, len(orphans))
	for _, o := range orphans {
		out = append(out, fmt.Sprintf("%s (%s)", o.Dirname, strings.Join(o.Modes, ",")))
	}
	return out
}

func aliasedMissingSites(reports []siteDrift) ([]string, int) {
	var sites []string
	total := 0
	for _, r := range reports {
		if len(r.Aliased) > 0 {
			sites = append(sites, string(r.Site))
			total += len(r.Aliased)
		}
	}
	return sites, total
}

func alertOnDrift(ctx context.Context, res sweepResult) error {
	reindex, tombstone, prune, aliased := res.totals()
	v := classifyDrift(res)
	if v.Op == "" {
		slog.InfoContext(ctx, "drift.clean",
			"sites", res.Stats.Sites,
			"r2_objects", res.Stats.R2Objects,
			"pg_deploys", res.Stats.PGDeploys,
			"indexed_total", res.Stats.IndexedTotal,
			"reclaimable", reindex+tombstone)
		return nil
	}
	slog.ErrorContext(ctx, "drift.detected",
		"op", v.Op,
		"sites", res.Stats.Sites,
		"read_failures", res.Stats.ReadFailures,
		"reindex", reindex,
		"tombstone", tombstone,
		"prune", prune,
		"aliased_missing", aliased,
		"orphan_aliases", len(res.OrphanAliases),
		"err", v.Err)
	captureBackground(v.Op, v.Err)
	if v.Fails {
		return v.Err
	}
	return nil
}
