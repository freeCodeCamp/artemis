package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/backfill"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

var (
	_ handler.SiteChangeEmitter = (*pg.Repo)(nil)
	_ handler.TombstoneStore    = (*pg.Repo)(nil)
	_ handler.RepoStore         = (*pg.RepoQueue)(nil)
	_ backfill.Lister           = (*r2.Client)(nil)
	_ backfill.Indexer          = (*pg.Repo)(nil)
	_ pg.SitesSource            = (*valkey.Store)(nil)
)

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

func newGCWiring(cfg *config.Config, repo *pg.Repo, r2c *r2.Client, metrics *gc.Metrics) (*gcWiring, error) {
	layout, err := newGCLayout(cfg.DeployPrefixFormat, cfg.Cleanup.TrashPrefix)
	if err != nil {
		return nil, err
	}
	return &gcWiring{
		Repo: repo,
		SiteGC: &gc.SiteGC{
			Store:        repo,
			Mover:        r2c,
			Policy:       gcPolicy(cfg.Cleanup),
			BlastCap:     cfg.Cleanup.BlastCap,
			DeployPrefix: layout.deployPrefix,
			TrashPrefix:  layout.trashPrefix,
			Now:          time.Now,
			Metrics:      metrics,
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
			Metrics:      metrics,
		},
		Purge: &gc.TombstonePurge{
			Store:     repo,
			Deleter:   r2c,
			Recovery:  time.Duration(cfg.Cleanup.RecoveryDays) * 24 * time.Hour,
			TrashBase: cfg.Cleanup.TrashPrefix,
			Now:       time.Now,
			Metrics:   metrics,
		},
	}, nil
}
