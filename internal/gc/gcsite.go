package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const destructiveMoveTimeout = 10 * time.Minute

type Store interface {
	DeploysForSite(ctx context.Context, site string) ([]Deploy, error)
	AliasTargets(ctx context.Context, site string) (targets map[string]struct{}, lastChange time.Time, err error)
	Tombstone(ctx context.Context, site string, d Deploy) error
}

type Mover interface {
	MovePrefix(ctx context.Context, src, dst string) (int, error)
}

type Locker interface {
	NewLockSession(ctx context.Context) (LockSession, error)
}

type LockSession interface {
	WithSiteLock(ctx context.Context, site string, fn func() error) error
	Close(ctx context.Context)
}

type SiteGC struct {
	Store        Store
	Mover        Mover
	Locker       Locker
	Policy       Policy
	BlastCap     int
	DeployPrefix func(site, id string) string
	TrashPrefix  func(site, id string) string
	LiveAliases  func(ctx context.Context, site string) (map[string]struct{}, error)
	Now          func() time.Time
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
	res.Aborted = plan.Aborted
	res.AbortReason = plan.Reason

	if dryRun {
		slog.InfoContext(ctx, "gc.site.dry-run", "site", site, "planned", len(res.Planned), "capped", plan.Aborted)
		return res, nil
	}

	if g.LiveAliases == nil {
		return res, fmt.Errorf("gc %s: live run without LiveAliases reader (wiring bug)", site)
	}
	if g.Locker == nil {
		return res, fmt.Errorf("gc %s: live run without site Locker (wiring bug)", site)
	}
	sessCtx, sessCancel := context.WithTimeout(context.WithoutCancel(ctx), destructiveMoveTimeout)
	sess, err := g.Locker.NewLockSession(sessCtx)
	sessCancel()
	if err != nil {
		return res, fmt.Errorf("gc %s: open lock session: %w", site, err)
	}
	defer sess.Close(ctx)
	for _, d := range plan.Delete {
		d := d
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destructiveMoveTimeout)
		err := sess.WithSiteLock(opCtx, site, func() error {
			live, err := g.LiveAliases(opCtx, site)
			if err != nil {
				return fmt.Errorf("re-read live aliases: %w", err)
			}
			if _, nowAliased := live[d.ID]; nowAliased {
				res.SkippedAliased = append(res.SkippedAliased, d.ID)
				return nil
			}
			src := g.DeployPrefix(site, d.ID)
			dst := g.TrashPrefix(site, d.ID)
			if _, err := g.Mover.MovePrefix(opCtx, src, dst); err != nil {
				return fmt.Errorf("tombstone-move %s: %w", d.ID, err)
			}
			if err := g.Store.Tombstone(opCtx, site, d); err != nil {
				return fmt.Errorf("record tombstone %s: %w", d.ID, err)
			}
			res.Tombstoned = append(res.Tombstoned, d.ID)
			res.BytesReclaimed += d.Bytes
			return nil
		})
		cancel()
		if err != nil {
			return res, fmt.Errorf("gc %s: %w", site, err)
		}
	}

	if plan.Aborted {
		slog.WarnContext(ctx, "gc.site.capped", "site", site,
			"tombstoned", len(res.Tombstoned), "reason", plan.Reason)
	}
	slog.InfoContext(ctx, "gc.site.done", "site", site,
		"planned", len(res.Planned),
		"tombstoned", len(res.Tombstoned),
		"skippedAliased", len(res.SkippedAliased),
		"bytes", res.BytesReclaimed)
	return res, nil
}
