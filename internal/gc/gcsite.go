package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Store interface {
	DeploysForSite(ctx context.Context, site string) ([]Deploy, error)
	AliasTargets(ctx context.Context, site string) (targets map[string]struct{}, lastChange time.Time, err error)
	Tombstone(ctx context.Context, site string, d Deploy) error
}

type Mover interface {
	MovePrefix(ctx context.Context, src, dst string) (int, error)
}

type SiteGC struct {
	Store        Store
	Mover        Mover
	Policy       Policy
	BlastCap     int
	DeployPrefix func(site, id string) string
	TrashPrefix  func(site, id string) string
	Now          func() time.Time
	Metrics      *Metrics
}

type GCResult struct {
	Site           string
	Planned        []string
	Tombstoned     []string
	SkippedAliased []string
	BytesReclaimed int64
	Aborted        bool
	AbortReason    string
	DryRun         bool
}

func (g *SiteGC) Run(ctx context.Context, site string, dryRun bool) (GCResult, error) {
	res := GCResult{Site: site, DryRun: dryRun}

	deploys, err := g.Store.DeploysForSite(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load deploys: %w", site, err)
	}
	targets, lastChange, err := g.Store.AliasTargets(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load aliases: %w", site, err)
	}

	plan := PlanSite(site, RetainInput{
		Deploys:         deploys,
		AliasTargets:    targets,
		LastAliasChange: lastChange,
		Now:             g.Now(),
	}, g.Policy, g.BlastCap)

	for _, d := range plan.Delete {
		res.Planned = append(res.Planned, d.ID)
	}
	if plan.Aborted {
		res.Aborted = true
		res.AbortReason = plan.Reason
		g.Metrics.run(WorkflowGCSiteLabel, "aborted")
		slog.Warn("gc.site.aborted", "site", site, "planned", len(res.Planned), "reason", plan.Reason)
		return res, nil
	}
	if dryRun {
		g.Metrics.run(WorkflowGCSiteLabel, "dry-run")
		slog.Info("gc.site.dry-run", "site", site, "planned", len(res.Planned))
		return res, nil
	}

	fresh, _, err := g.Store.AliasTargets(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: re-read aliases: %w", site, err)
	}
	for _, d := range plan.Delete {
		if _, nowAliased := fresh[d.ID]; nowAliased {
			res.SkippedAliased = append(res.SkippedAliased, d.ID)
			continue
		}
		src := g.DeployPrefix(site, d.ID)
		dst := g.TrashPrefix(site, d.ID)
		if _, err := g.Mover.MovePrefix(ctx, src, dst); err != nil {
			return res, fmt.Errorf("gc %s: tombstone-move %s: %w", site, d.ID, err)
		}
		if err := g.Store.Tombstone(ctx, site, d); err != nil {
			return res, fmt.Errorf("gc %s: record tombstone %s: %w", site, d.ID, err)
		}
		res.Tombstoned = append(res.Tombstoned, d.ID)
		res.BytesReclaimed += d.Bytes
	}

	g.Metrics.tombstoned(len(res.Tombstoned))
	g.Metrics.run(WorkflowGCSiteLabel, "ok")
	slog.Info("gc.site.done", "site", site,
		"planned", len(res.Planned),
		"tombstoned", len(res.Tombstoned),
		"skippedAliased", len(res.SkippedAliased),
		"bytes", res.BytesReclaimed)
	return res, nil
}
