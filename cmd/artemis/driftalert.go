package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

const (
	opDriftSweep          = "drift.sweep"
	opDriftSelfCheck      = "drift.selfcheck"
	opDriftUnreadable     = "drift.unreadable"
	opDriftAliasedMissing = "drift.aliased_missing"
)

type driftVerdict struct {
	Op    string
	Err   error
	Fails bool
}

func classifyDrift(res sweepResult) driftVerdict {
	unread := unreadableErr(unreadableSites(res.Reports), res.Stats.Sites)
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
	return driftVerdict{}
}

func unreadableErr(unreadable []string, sites int) error {
	if len(unreadable) == 0 {
		return nil
	}
	return fmt.Errorf("drift sweep could not read %d of %d sites (%s): unknown drift, not zero drift",
		len(unreadable), sites, strings.Join(unreadable, ", "))
}

func unreadableSites(reports []siteDrift) []string {
	var out []string
	for _, r := range reports {
		if r.FailedWith != nil {
			out = append(out, r.Site)
		}
	}
	return out
}

func aliasedMissingSites(reports []siteDrift) ([]string, int) {
	var sites []string
	total := 0
	for _, r := range reports {
		if len(r.Aliased) > 0 {
			sites = append(sites, r.Site)
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
		"err", v.Err)
	captureBackground(v.Op, v.Err)
	if v.Fails {
		return v.Err
	}
	return nil
}
