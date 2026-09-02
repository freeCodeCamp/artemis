package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/backfill"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type auditRecorder interface {
	RecordAudit(ctx context.Context, e pg.AuditEvent) error
}

var captureAuditFailure = observability.CaptureBackground

var (
	_ handler.TombstoneStore    = (*pg.Repo)(nil)
	_ handler.TrashStore        = (*pg.Repo)(nil)
	_ handler.DeployIndexWriter = (*pg.Repo)(nil)
	_ handler.SiteLocker        = (*pg.Repo)(nil)
	_ handler.AuditStore        = (*pg.Repo)(nil)
	_ handler.RepoStore         = (*pg.RepoQueue)(nil)
	_ backfill.Lister           = (*r2.Client)(nil)
	_ backfill.Indexer          = (*pg.Repo)(nil)
	_ pg.SitesSource            = (*valkey.Store)(nil)
	_ reservationWiring         = (*pg.RegistryStore)(nil)
)

func wirePGRepo(h *handler.Handlers, repo *pg.Repo) {
	if repo == nil {
		return
	}
	h.Tombstones = repo
	h.Trash = repo
	h.Index = repo
	h.Pending = repo
	h.Locker = repo
	h.Audit = repo
}

type siteSlugFn func(dirname sitekey.Dirname) (sitekey.Slug, bool)

// auditSite converts the gc keyspace (storage dirname) to the registry
// slug every audit_log reader queries by. An unmappable dirname keeps
// its raw value and flags the row, because audit_log is append-only:
// a dropped row can never be repaired, an odd one can be read around.
func auditSite(toSlug siteSlugFn, site sitekey.Dirname) (string, map[string]any) {
	if toSlug == nil {
		return string(site), nil
	}
	slug, ok := toSlug(site)
	if !ok {
		captureAuditFailure("audit.site_unmapped", fmt.Errorf("audit: site %q renders from no slug", site))
		return string(site), map[string]any{"site_unmapped": true}
	}
	return string(slug), nil
}

type gcPurgeAuditor struct {
	repo   auditRecorder
	toSlug siteSlugFn
}

func (a gcPurgeAuditor) RecordPurge(ctx context.Context, site sitekey.Dirname, deployID string) error {
	slug, detail := auditSite(a.toSlug, site)
	err := a.repo.RecordAudit(ctx, pg.AuditEvent{
		Actor:    "system:gc",
		Action:   "gc.purge",
		Site:     slug,
		DeployID: deployID,
		Outcome:  "success",
		Detail:   detail,
	})
	if err != nil {
		captureAuditFailure("audit.record", err)
	}
	return err
}

type gcTombstoneAuditor struct {
	repo   auditRecorder
	actor  string
	action string
	toSlug siteSlugFn
}

func (a gcTombstoneAuditor) AuditTombstone(ctx context.Context, site sitekey.Dirname, id string) error {
	slug, detail := auditSite(a.toSlug, site)
	err := a.repo.RecordAudit(ctx, pg.AuditEvent{
		Actor:    a.actor,
		Action:   a.action,
		Site:     slug,
		DeployID: id,
		Outcome:  "success",
		Detail:   detail,
	})
	if err != nil {
		captureAuditFailure("audit.record", err)
	}
	return err
}

func openRepoQueue(pgDB *pg.DB) (handler.RepoStore, error) {
	if pgDB == nil {
		return nil, fmt.Errorf("repo-creation feature requires DATABASE_URL")
	}
	return pg.NewRepoQueue(pgDB), nil
}

const deployIDToken = "<ts>-<sha>"

type gcLayout struct {
	sitePrefix   func(site sitekey.Dirname) string
	deployPrefix func(site sitekey.Dirname, id string) string
	trashPrefix  func(site sitekey.Dirname, id string) string
}

func newGCLayout(format, trashBase string) (gcLayout, error) {
	idx := strings.Index(format, deployIDToken)
	if idx < 0 {
		return gcLayout{}, fmt.Errorf("DEPLOY_PREFIX_FORMAT %q must contain %s", format, deployIDToken)
	}
	head := format[:idx]
	tail := format[idx+len(deployIDToken):]
	slash := strings.IndexByte(head, '/')
	if slash < 0 {
		return gcLayout{}, fmt.Errorf("DEPLOY_PREFIX_FORMAT %q must contain '/' after the site segment", format)
	}
	subPath := head[slash+1:]
	if trashBase == "" {
		trashBase = "_trash/"
	}
	return gcLayout{
		sitePrefix: func(site sitekey.Dirname) string { return string(site) + "/" + subPath },
		deployPrefix: func(site sitekey.Dirname, id string) string {
			p := string(site) + "/" + subPath + id + tail
			if !strings.HasSuffix(p, "/") {
				p += "/"
			}
			return p
		},
		trashPrefix: func(site sitekey.Dirname, id string) string {
			return trashBase + string(site) + "/" + id + "/"
		},
	}, nil
}

type aliasGetter interface {
	GetAlias(ctx context.Context, aliasKey string) (string, error)
}

func newLiveAliasReader(getter aliasGetter, tails ...string) func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
	return func(ctx context.Context, dirname sitekey.Dirname) (map[string]struct{}, error) {
		out := map[string]struct{}{}
		for _, tail := range tails {
			v, err := getter.GetAlias(ctx, string(dirname)+"/"+tail)
			if err != nil {
				if r2.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			if v = strings.TrimSpace(v); v != "" {
				out[v] = struct{}{}
			}
		}
		return out, nil
	}
}

func gcPolicy(c config.CleanupConfig) gc.Policy {
	return gc.Policy{
		RecentKeep:    c.RecentKeep,
		Grace:         c.Grace,
		Retention:     time.Duration(c.RetentionDays) * 24 * time.Hour,
		ServeCacheTTL: c.ServeCacheTTL,
	}
}

type outboxPurger interface {
	PurgeOutbox(ctx context.Context, before time.Time, limit int, dryRun bool) (int, error)
}

type gcWiring struct {
	Repo            *pg.Repo
	SiteGC          *gc.SiteGC
	Reconciler      *gc.Reconciler
	Purge           *gc.TombstonePurge
	Reservations    reclaimableSource
	Lifecycle       lifecycleEmitter
	Reclaim         reclaimDeps
	Outbox          outboxPurger
	OutboxRetention time.Duration
	PendingSites    pendingSiteSource
}

func expiredClaimChecker(src expiredClaimSource) func(context.Context, sitekey.Slug) (bool, error) {
	if src == nil {
		return nil
	}
	return src.IsExpiredReservation
}

func reclaimClaimerOf(w reservationWiring) reclaimClaimer {
	if w == nil {
		return nil
	}
	return w
}

func auditedReleaserOf(w reservationWiring) auditedReleaser {
	if w == nil {
		return nil
	}
	return w
}

func heldChecker(src heldNameSource, toSlug func(sitekey.Dirname) (sitekey.Slug, bool)) func(context.Context, sitekey.Dirname) (bool, error) {
	if src == nil {
		return nil
	}
	return func(ctx context.Context, site sitekey.Dirname) (bool, error) {
		slug, ok := toSlug(site)
		if !ok {
			slog.WarnContext(ctx, "gc.held.dirname_unmappable",
				"site", site,
				"detail", "the reservation guard cannot answer for a dirname the deploy-prefix template does not invert; this site collects unguarded")
			return false, nil
		}
		return src.IsHeld(ctx, slug)
	}
}

func newGCWiring(cfg *config.Config, repo *pg.Repo, r2c *r2.Client, writer registry.Writer) (*gcWiring, error) {
	layout, err := newGCLayout(cfg.DeployPrefixFormat, cfg.Cleanup.TrashPrefix)
	if err != nil {
		return nil, err
	}
	tails, err := cfg.AliasKeyTails()
	if err != nil {
		return nil, err
	}
	liveAliases := newLiveAliasReader(r2c, tails...)
	tmpl, err := handler.NewDeployPrefixTemplate(cfg.DeployPrefixFormat)
	if err != nil {
		return nil, err
	}
	toSlug := siteSlugFn(tmpl.SiteSlug)
	var outbox outboxPurger
	var lifecycle lifecycleEmitter
	if repo != nil {
		outbox = repo
		lifecycle = repo
	}
	var pendingSites pendingSiteSource
	var gcStore gc.Store
	var gcLocker gc.Locker
	var reconcileStore gc.ReconcileStore
	var reaper gc.TombstoneReaper
	var siteLocker gc.SiteLocker
	var tombstoneRecorder siteTombstoneRecorder
	var pending gc.PendingSource
	var pendingIDs func(context.Context, sitekey.Dirname) (map[string]struct{}, error)
	if repo != nil {
		pendingSites = repo
		pending = repo
		pendingIDs = repo.PendingDeployIDs
		gcStore = repo
		gcLocker = repo
		reconcileStore = repo
		reaper = repo
		siteLocker = repo
		tombstoneRecorder = repo
	}
	resv, _ := writer.(reservationWiring)

	return &gcWiring{
		Repo:         repo,
		Reservations: resv,
		Lifecycle:    lifecycle,
		Reclaim: reclaimDeps{
			Mover:     r2c,
			Tombstone: tombstoneRecorder,
			Locker:    gcLocker,
			Claim:     expiredClaimChecker(resv),
			Claimer:   reclaimClaimerOf(resv),
			Releaser:  auditedReleaserOf(resv),
			Dirname:   tmpl.SiteDirname,
			TrashBase: cfg.Cleanup.TrashPrefix,
		},
		Outbox:          outbox,
		OutboxRetention: time.Duration(cfg.Cleanup.OutboxRetentionDays) * 24 * time.Hour,
		SiteGC: &gc.SiteGC{
			Store:        gcStore,
			Mover:        r2c,
			Locker:       gcLocker,
			LiveAliases:  liveAliases,
			Pending:      pending,
			PendingIDs:   pendingIDs,
			Policy:       gcPolicy(cfg.Cleanup),
			BlastCap:     cfg.Cleanup.BlastCap,
			DeployPrefix: layout.deployPrefix,
			TrashPrefix:  layout.trashPrefix,
			Now:          time.Now,
			Held:         heldChecker(resv, tmpl.SiteSlug),
			Audit:        gcTombstoneAuditor{repo: repo, actor: "system:gc", action: "gc.tombstone", toSlug: toSlug},
		},
		Reconciler: &gc.Reconciler{
			Lister:       r2c,
			Store:        reconcileStore,
			Mover:        r2c,
			Locker:       gcLocker,
			Grace:        cfg.Cleanup.Grace,
			BlastCap:     cfg.Cleanup.BlastCap,
			SitePrefix:   layout.sitePrefix,
			DeployPrefix: layout.deployPrefix,
			TrashPrefix:  layout.trashPrefix,
			LiveAliases:  liveAliases,
			PendingIDs:   pendingIDs,
			Now:          time.Now,
			Audit:        gcTombstoneAuditor{repo: repo, actor: "system:reconcile", action: "gc.reconcile", toSlug: toSlug},
			PruneAudit:   gcTombstoneAuditor{repo: repo, actor: "system:reconcile", action: "gc.reconcile.prune", toSlug: toSlug},
		},
		PendingSites: pendingSites,
		Purge: &gc.TombstonePurge{
			Store:     reaper,
			Deleter:   r2c,
			Recovery:  time.Duration(cfg.Cleanup.RecoveryDays) * 24 * time.Hour,
			TrashBase: cfg.Cleanup.TrashPrefix,
			BlastCap:  cfg.Cleanup.BlastCap,
			Now:       time.Now,
			Locker:    siteLocker,
			Audit:     gcPurgeAuditor{repo: repo, toSlug: toSlug},
		},
	}, nil
}
