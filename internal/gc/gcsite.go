package gc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const destructiveMoveTimeout = 10 * time.Minute

var errHeldUnderLock = errors.New("gc: site reserved under the lock")

type Store interface {
	DeploysForSite(ctx context.Context, site sitekey.Dirname) ([]Deploy, error)
	AliasTargets(ctx context.Context, site sitekey.Dirname) (targets map[string]struct{}, lastChange time.Time, err error)
	Tombstone(ctx context.Context, site sitekey.Dirname, d Deploy) error
}

type PendingSource interface {
	ExpiredPendingDeploys(ctx context.Context, site sitekey.Dirname, before time.Time) ([]Deploy, error)
}

type Mover interface {
	MovePrefix(ctx context.Context, src, dst string) (int, error)
}

type Locker interface {
	NewLockSession(ctx context.Context) (LockSession, error)
}

type GCAuditor interface {
	AuditTombstone(ctx context.Context, site sitekey.Dirname, id string) error
}

type LockSession interface {
	WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error
	Close(ctx context.Context)
}

type SiteGC struct {
	Store        Store
	Mover        Mover
	Locker       Locker
	Policy       Policy
	BlastCap     int
	DeployPrefix func(site sitekey.Dirname, id string) string
	TrashPrefix  func(site sitekey.Dirname, id string) string
	LiveAliases  func(ctx context.Context, site sitekey.Dirname) (map[string]struct{}, error)
	Now          func() time.Time
	Audit        GCAuditor
	Pending      PendingSource
	PendingIDs   func(ctx context.Context, site sitekey.Dirname) (map[string]struct{}, error)
	Held         func(ctx context.Context, site sitekey.Dirname) (bool, error)
}

type GCResult struct {
	Site              sitekey.Dirname
	Planned           []string
	Tombstoned        []string
	SkippedAliased    []string
	SkippedNotPending []string
	BytesReclaimed    int64
	Aborted           bool
	AbortReason       string
	DryRun            bool
	Held              bool
}

func (g *SiteGC) expiredPending(ctx context.Context, site sitekey.Dirname) ([]Deploy, error) {
	if g.Pending == nil {
		return nil, nil
	}
	return g.Pending.ExpiredPendingDeploys(ctx, site, g.Now().Add(-g.Policy.Grace))
}

func (g *SiteGC) heldNow(ctx context.Context, site sitekey.Dirname, res *GCResult) (bool, error) {
	if g.Held == nil {
		return false, nil
	}
	held, err := g.Held(ctx, site)
	if err != nil {
		return false, fmt.Errorf("gc %s: reservation state unreadable: %w", site, err)
	}
	if held {
		res.Held = true
		slog.InfoContext(ctx, "gc.site.held", "site", site,
			"detail", "the name is inside its reservation grace; collecting now would trash the bytes undelete restores")
	}
	return held, nil
}

func (g *SiteGC) Run(ctx context.Context, site sitekey.Dirname, dryRun bool) (GCResult, error) {
	res := GCResult{Site: site, DryRun: dryRun}

	held, err := g.heldNow(ctx, site, &res)
	if err != nil {
		return res, err
	}
	if held {
		return res, nil
	}

	deploys, err := g.Store.DeploysForSite(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load deploys: %w", site, err)
	}
	targets, lastChange, err := g.Store.AliasTargets(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load aliases: %w", site, err)
	}

	expired, err := g.expiredPending(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load pending deploys: %w", site, err)
	}

	plan := PlanSite(site, RetainInput{
		Deploys:         deploys,
		Expired:         expired,
		AliasTargets:    targets,
		LastAliasChange: lastChange,
		Now:             g.Now(),
	}, g.Policy, g.BlastCap)

	return g.execute(ctx, site, plan, dryRun, res, g.revalidatePending(expired))
}

func (g *SiteGC) revalidatePending(expired []Deploy) func(context.Context, sitekey.Dirname, string) (bool, error) {
	if g.PendingIDs == nil || len(expired) == 0 {
		return nil
	}
	fromPending := make(map[string]struct{}, len(expired))
	for _, d := range expired {
		fromPending[d.ID] = struct{}{}
	}
	return func(ctx context.Context, site sitekey.Dirname, id string) (bool, error) {
		if _, ok := fromPending[id]; !ok {
			return true, nil
		}
		return g.stillPending(ctx, site, id)
	}
}

func (g *SiteGC) SweepPending(ctx context.Context, site sitekey.Dirname, dryRun bool) (GCResult, error) {
	res := GCResult{Site: site, DryRun: dryRun}

	held, err := g.heldNow(ctx, site, &res)
	if err != nil {
		return res, err
	}
	if held {
		return res, nil
	}

	expired, err := g.expiredPending(ctx, site)
	if err != nil {
		return res, fmt.Errorf("gc %s: load pending deploys: %w", site, err)
	}
	if len(expired) == 0 {
		return res, nil
	}

	if g.PendingIDs == nil {
		return res, fmt.Errorf("gc %s: pending sweep without a PendingIDs reader (wiring bug)", site)
	}

	return g.execute(ctx, site, PlanSite(site, RetainInput{Expired: expired, Now: g.Now()}, g.Policy, g.BlastCap), dryRun, res, g.stillPending)
}

func (g *SiteGC) stillPending(ctx context.Context, site sitekey.Dirname, id string) (bool, error) {
	ids, err := g.PendingIDs(ctx, site)
	if err != nil {
		return false, err
	}
	_, ok := ids[id]
	return ok, nil
}

func (g *SiteGC) execute(ctx context.Context, site sitekey.Dirname, plan Plan, dryRun bool, res GCResult,
	revalidate func(context.Context, sitekey.Dirname, string) (bool, error),
) (GCResult, error) {
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
		var tombstoned bool
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destructiveMoveTimeout)
		err := sess.WithSiteLock(opCtx, site, func(opCtx context.Context) error {
			if g.Held != nil {
				held, err := g.Held(opCtx, site)
				if err != nil {
					return fmt.Errorf("re-read reservation state: %w", err)
				}
				if held {
					res.Held = true
					return errHeldUnderLock
				}
			}
			if revalidate != nil {
				ok, err := revalidate(opCtx, site, d.ID)
				if err != nil {
					return fmt.Errorf("re-read pending state: %w", err)
				}
				if !ok {
					res.SkippedNotPending = append(res.SkippedNotPending, d.ID)
					return nil
				}
			}
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
			if err := g.Store.Tombstone(opCtx, site, d); err != nil {
				return fmt.Errorf("record tombstone %s: %w", d.ID, err)
			}
			if _, err := g.Mover.MovePrefix(opCtx, src, dst); err != nil {
				slog.WarnContext(opCtx, "gc.site.tombstone_move_deferred",
					"site", site, "deploy_id", d.ID, "trash_prefix", dst, "err", err,
					"detail", "the row landed before the move, so the bytes stay at the deploy prefix; the "+
						"tombstone blocks reindex until tombstone-purge clears it after the recovery window")
				return fmt.Errorf("tombstone-move %s: %w", d.ID, err)
			}
			res.Tombstoned = append(res.Tombstoned, d.ID)
			res.BytesReclaimed += d.Bytes
			tombstoned = true
			return nil
		})
		cancel()
		if errors.Is(err, errHeldUnderLock) {
			slog.InfoContext(ctx, "gc.site.held", "site", site,
				"detail", "the name was reserved between the pre-lock check and the site lock; nothing further is collected")
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("gc %s: %w", site, err)
		}
		if tombstoned && g.Audit != nil {
			auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), destructiveMoveTimeout)
			if aErr := g.Audit.AuditTombstone(auditCtx, site, d.ID); aErr != nil {
				slog.WarnContext(auditCtx, "gc.site.audit_failed", "site", site, "deploy_id", d.ID, "err", aErr)
			}
			auditCancel()
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
		"skippedNotPending", len(res.SkippedNotPending),
		"bytes", res.BytesReclaimed)
	return res, nil
}
