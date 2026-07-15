package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/backfill"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

type auditRecorder interface {
	RecordAudit(ctx context.Context, e pg.AuditEvent) error
}

var captureAuditFailure = observability.CaptureBackground

var (
	_ handler.SiteChangeEmitter = (*pg.Repo)(nil)
	_ handler.TombstoneStore    = (*pg.Repo)(nil)
	_ handler.TrashStore        = (*pg.Repo)(nil)
	_ handler.DeployIndexWriter = (*pg.Repo)(nil)
	_ handler.SiteLocker        = (*pg.Repo)(nil)
	_ handler.AuditStore        = (*pg.Repo)(nil)
	_ handler.RepoStore         = (*pg.RepoQueue)(nil)
	_ backfill.Lister           = (*r2.Client)(nil)
	_ backfill.Indexer          = (*pg.Repo)(nil)
	_ pg.SitesSource            = (*valkey.Store)(nil)
)

func wirePGRepo(h *handler.Handlers, repo *pg.Repo) {
	if repo == nil {
		return
	}
	h.Outbox = repo
	h.Tombstones = repo
	h.Trash = repo
	h.Index = repo
	h.Locker = repo
	h.Audit = repo
}

type gcPurgeAuditor struct{ repo auditRecorder }

func (a gcPurgeAuditor) RecordPurge(ctx context.Context, site, deployID string) error {
	err := a.repo.RecordAudit(ctx, pg.AuditEvent{
		Actor:    "system:gc",
		Action:   "gc.purge",
		Site:     site,
		DeployID: deployID,
		Outcome:  "success",
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
}

func (a gcTombstoneAuditor) AuditTombstone(ctx context.Context, site, id string) error {
	err := a.repo.RecordAudit(ctx, pg.AuditEvent{
		Actor:    a.actor,
		Action:   a.action,
		Site:     site,
		DeployID: id,
		Outcome:  "success",
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
	sitePrefix   func(site string) string
	deployPrefix func(site, id string) string
	trashPrefix  func(site, id string) string
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
		sitePrefix: func(site string) string { return site + "/" + subPath },
		deployPrefix: func(site, id string) string {
			p := site + "/" + subPath + id + tail
			if !strings.HasSuffix(p, "/") {
				p += "/"
			}
			return p
		},
		trashPrefix: func(site, id string) string { return trashBase + site + "/" + id + "/" },
	}, nil
}

type aliasGetter interface {
	GetAlias(ctx context.Context, aliasKey string) (string, error)
}

func newLiveAliasReader(getter aliasGetter, formats ...string) (func(context.Context, string) (map[string]struct{}, error), error) {
	fmts := make([]string, 0, len(formats))
	for _, f := range formats {
		if !strings.Contains(f, "<site>") {
			return nil, fmt.Errorf("alias key format %q must contain <site>", f)
		}
		fmts = append(fmts, f)
	}
	return func(ctx context.Context, site string) (map[string]struct{}, error) {
		out := map[string]struct{}{}
		for _, f := range fmts {
			v, err := getter.GetAlias(ctx, strings.ReplaceAll(f, "<site>", site))
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
	}, nil
}

func gcPolicy(c config.CleanupConfig) gc.Policy {
	return gc.Policy{
		RecentKeep:    c.RecentKeep,
		Grace:         c.Grace,
		Retention:     time.Duration(c.RetentionDays) * 24 * time.Hour,
		ServeCacheTTL: c.ServeCacheTTL,
	}
}

type gcWiring struct {
	Repo       *pg.Repo
	SiteGC     *gc.SiteGC
	Reconciler *gc.Reconciler
	Purge      *gc.TombstonePurge
}

func newGCWiring(cfg *config.Config, repo *pg.Repo, r2c *r2.Client) (*gcWiring, error) {
	layout, err := newGCLayout(cfg.DeployPrefixFormat, cfg.Cleanup.TrashPrefix)
	if err != nil {
		return nil, err
	}
	liveAliases, err := newLiveAliasReader(r2c, cfg.Aliases.ProductionKeyFormat, cfg.Aliases.PreviewKeyFormat)
	if err != nil {
		return nil, err
	}
	return &gcWiring{
		Repo: repo,
		SiteGC: &gc.SiteGC{
			Store:        repo,
			Mover:        r2c,
			Locker:       repo,
			LiveAliases:  liveAliases,
			Policy:       gcPolicy(cfg.Cleanup),
			BlastCap:     cfg.Cleanup.BlastCap,
			DeployPrefix: layout.deployPrefix,
			TrashPrefix:  layout.trashPrefix,
			Now:          time.Now,
			Audit:        gcTombstoneAuditor{repo: repo, actor: "system:gc", action: "gc.tombstone"},
		},
		Reconciler: &gc.Reconciler{
			Lister:       r2c,
			Store:        repo,
			Mover:        r2c,
			Grace:        cfg.Cleanup.Grace,
			SitePrefix:   layout.sitePrefix,
			DeployPrefix: layout.deployPrefix,
			TrashPrefix:  layout.trashPrefix,
			Now:          time.Now,
			Audit:        gcTombstoneAuditor{repo: repo, actor: "system:reconcile", action: "gc.reconcile"},
		},
		Purge: &gc.TombstonePurge{
			Store:     repo,
			Deleter:   r2c,
			Recovery:  time.Duration(cfg.Cleanup.RecoveryDays) * 24 * time.Hour,
			TrashBase: cfg.Cleanup.TrashPrefix,
			Now:       time.Now,
			Locker:    repo,
			Audit:     gcPurgeAuditor{repo: repo},
		},
	}, nil
}
