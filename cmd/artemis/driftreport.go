package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const driftReportCommand = "driftreport"

var errReadOnlyViolation = errors.New("drift report attempted a write: this binary is wired read-only")

type readOnlyStore struct{ inner gc.ReconcileStore }

func (s readOnlyStore) DeploysForSite(ctx context.Context, site sitekey.Dirname) ([]gc.Deploy, error) {
	return s.inner.DeploysForSite(ctx, site)
}

func (s readOnlyStore) AliasTargets(ctx context.Context, site sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	return s.inner.AliasTargets(ctx, site)
}

func (readOnlyStore) ReindexDeploy(context.Context, sitekey.Dirname, string, time.Time, bool) (bool, error) {
	return false, errReadOnlyViolation
}

func (readOnlyStore) RecordTombstone(context.Context, sitekey.Dirname, string, int64) error {
	return errReadOnlyViolation
}

func (readOnlyStore) PruneDeploy(context.Context, sitekey.Dirname, string) error {
	return errReadOnlyViolation
}

type readOnlyMover struct{}

func (readOnlyMover) MovePrefix(context.Context, string, string) (int, error) {
	return 0, errReadOnlyViolation
}

type countingLister struct {
	inner   gc.ReconcileLister
	objects int
}

func (l *countingLister) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	keys, err := l.inner.ListPrefix(ctx, prefix)
	l.objects += len(keys)
	return keys, err
}

type countingStore struct {
	gc.ReconcileStore
	deploys int
}

func (s *countingStore) DeploysForSite(ctx context.Context, site sitekey.Dirname) ([]gc.Deploy, error) {
	out, err := s.ReconcileStore.DeploysForSite(ctx, site)
	s.deploys += len(out)
	return out, err
}

type sweepStats struct {
	Scoped bool
	Sites  int
	// ReadFailures silences the misconfiguration checks below: a sweep that
	// could not read is not evidence that the site list is wrong.
	ReadFailures int
	// IndexedTotal is counted straight from the deploys table, independent of
	// the site list, so a wrong site list cannot also shrink the denominator
	// that is supposed to expose it.
	IndexedTotal int
	R2Objects    int
	PGDeploys    int
	Prunes       int
}

func (s sweepStats) validate() error {
	if s.Sites == 0 {
		return errors.New("swept 0 sites: neither the registry nor the index named anything to check")
	}
	if !s.Scoped && s.ReadFailures == 0 && s.IndexedTotal > 0 && s.PGDeploys == 0 {
		return fmt.Errorf("the deploys table holds %d active rows but the %d swept sites matched none of them: "+
			"the site list is wrong, so this sweep saw nothing it was meant to check",
			s.IndexedTotal, s.Sites)
	}
	if !s.Scoped && s.ReadFailures == 0 && s.PGDeploys > 0 && s.R2Objects == 0 {
		return fmt.Errorf("postgres knows %d deploys across %d sites but the R2 listing returned 0 objects: "+
			"the sweep is reading a prefix no write path produces, so this is a misconfiguration, not zero drift",
			s.PGDeploys, s.Sites)
	}
	if s.PGDeploys > 0 && s.Prunes*2 > s.PGDeploys {
		return fmt.Errorf("the plan would delete %d of %d index rows: "+
			"refusing to report a sweep that disagrees with more than half of postgres",
			s.Prunes, s.PGDeploys)
	}
	return nil
}

type siteDrift struct {
	Site       sitekey.Dirname
	Reindex    []string
	Tombstone  []string
	Prune      []string
	Aliased    []string
	Capped     bool
	FailedWith error
}

func (d siteDrift) total() int {
	return len(d.Reindex) + len(d.Tombstone) + len(d.Prune) + len(d.Aliased)
}

type siteDirnameReader interface {
	KnownSiteDirnames(ctx context.Context) ([]sitekey.Dirname, error)
}

type registrySiteReader interface {
	Sites(ctx context.Context) ([]registry.Site, error)
}

type driftSweepRepo interface {
	gc.ReconcileStore
	siteDirnameReader
	CountDeploys(ctx context.Context) (int, error)
}

type sweepResult struct {
	Reports []siteDrift
	Stats   sweepStats
}

func (r sweepResult) totals() (reindex, tombstone, prune, aliased int) {
	for _, s := range r.Reports {
		reindex += len(s.Reindex)
		tombstone += len(s.Tombstone)
		prune += len(s.Prune)
		aliased += len(s.Aliased)
	}
	return reindex, tombstone, prune, aliased
}

type driftSweep struct {
	rc     *gc.Reconciler
	lister *countingLister
	store  *countingStore
	repo   driftSweepRepo
	reg    registrySiteReader
	tmpl   handler.DeployPrefixTemplate
}

func newReadOnlySweeper(base *gc.Reconciler, lister gc.ReconcileLister, repo driftSweepRepo,
	reg registrySiteReader, tmpl handler.DeployPrefixTemplate) *driftSweep {
	counting := &countingLister{inner: lister}
	store := &countingStore{ReconcileStore: readOnlyStore{inner: repo}}

	rc := *base
	rc.Lister = counting
	rc.Store = store
	rc.Mover = readOnlyMover{}
	rc.Locker = nil
	rc.Audit = nil
	rc.PruneAudit = nil

	return &driftSweep{rc: &rc, lister: counting, store: store, repo: repo, reg: reg, tmpl: tmpl}
}

func (s *driftSweep) Run(ctx context.Context) (sweepResult, error) {
	sites, err := driftReportSites(ctx, s.repo, s.reg, s.tmpl)
	if err != nil {
		return sweepResult{}, fmt.Errorf("enumerate sites: %w", err)
	}
	return s.sweep(ctx, sites, false)
}

func (s *driftSweep) runSite(ctx context.Context, site sitekey.Dirname) (sweepResult, error) {
	return s.sweep(ctx, []sitekey.Dirname{site}, true)
}

func (s *driftSweep) sweep(ctx context.Context, sites []sitekey.Dirname, scoped bool) (sweepResult, error) {
	reports := make([]siteDrift, 0, len(sites))
	for _, site := range sites {
		if err := ctx.Err(); err != nil {
			return sweepResult{}, err
		}
		rep, runErr := s.rc.ReconcileSite(ctx, site, true)
		reports = append(reports, siteDrift{
			Site:       site,
			Reindex:    rep.Reindexed,
			Tombstone:  rep.OrphanTombstoned,
			Prune:      rep.PGPruned,
			Aliased:    rep.AliasedMissing,
			Capped:     rep.Capped,
			FailedWith: runErr,
		})
	}

	indexedTotal, err := s.repo.CountDeploys(ctx)
	if err != nil {
		return sweepResult{}, fmt.Errorf("count deploys: %w", err)
	}

	stats := sweepStats{
		Scoped:       scoped,
		Sites:        len(sites),
		IndexedTotal: indexedTotal,
		R2Objects:    s.lister.objects,
		PGDeploys:    s.store.deploys,
	}
	for _, r := range reports {
		stats.Prunes += len(r.Prune)
	}
	stats.ReadFailures = countReadFailures(reports)
	return sweepResult{Reports: reports, Stats: stats}, nil
}

func countReadFailures(reports []siteDrift) int {
	n := 0
	for _, r := range reports {
		if r.FailedWith != nil {
			n++
		}
	}
	return n
}

func driftReportSites(ctx context.Context, repo siteDirnameReader, reg registrySiteReader, tmpl handler.DeployPrefixTemplate) ([]sitekey.Dirname, error) {
	known, err := repo.KnownSiteDirnames(ctx)
	if err != nil {
		return nil, err
	}
	sites, err := reg.Sites(ctx)
	if err != nil {
		return nil, err
	}
	slugs := make([]sitekey.Slug, 0, len(sites))
	for _, s := range sites {
		slugs = append(slugs, s.Slug)
	}
	all := append(known, storageSiteNames(slugs, tmpl)...)
	slices.Sort(all)
	return slices.Compact(all), nil
}

func runDriftReport(ctx context.Context, out io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required for a drift report")
	}

	db, err := pg.New(ctx, pg.Config{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer db.Close()

	r2Client, err := r2.New(ctx, r2.Config{
		Endpoint:        cfg.R2.Endpoint,
		AccessKeyID:     cfg.R2.AccessKeyID,
		SecretAccessKey: cfg.R2.SecretAccessKey,
		Bucket:          cfg.R2.Bucket,
		Region:          "auto",
	})
	if err != nil {
		return fmt.Errorf("connect r2: %w", err)
	}

	repo := pg.NewRepo(db)
	wiring, err := newGCWiring(cfg, repo, r2Client)
	if err != nil {
		return fmt.Errorf("wire gc: %w", err)
	}

	tmpl, err := handler.NewDeployPrefixTemplate(cfg.DeployPrefixFormat)
	if err != nil {
		return fmt.Errorf("deploy prefix format: %w", err)
	}

	res, err := newReadOnlySweeper(wiring.Reconciler, r2Client, repo, pg.NewRegistryStore(db), tmpl).Run(ctx)
	if err != nil {
		return err
	}
	return finishReport(out, res.Reports, cfg.Cleanup, res.Stats)
}

func finishReport(out io.Writer, reports []siteDrift, cleanup config.CleanupConfig, stats sweepStats) error {
	stats.ReadFailures = countReadFailures(reports)
	reportErr := writeDriftReport(out, reports, cleanup)
	fmt.Fprintf(out, "SWEPT sites=%d r2-objects=%d pg-deploys=%d/%d read-failures=%d\n",
		stats.Sites, stats.R2Objects, stats.PGDeploys, stats.IndexedTotal, stats.ReadFailures)
	if err := stats.validate(); err != nil {
		return errors.Join(fmt.Errorf("sweep self-check: %w", err), reportErr)
	}
	return reportErr
}

func writeDriftReport(out io.Writer, reports []siteDrift, cleanup config.CleanupConfig) error {
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].total() > reports[j].total() })

	var reindex, tombstone, prune, aliased, failed int
	fmt.Fprintf(out, "drift report (read-only, no writes possible)\n")
	fmt.Fprintf(out, "grace=%s blast-cap=%d sites=%d\n\n", cleanup.Grace, cleanup.BlastCap, len(reports))
	fmt.Fprintf(out, "%-42s %8s %10s %6s %8s\n", "SITE", "REINDEX", "TOMBSTONE", "PRUNE", "ALIASED")

	for _, r := range reports {
		reindex += len(r.Reindex)
		tombstone += len(r.Tombstone)
		prune += len(r.Prune)
		aliased += len(r.Aliased)
		if r.FailedWith != nil {
			failed++
			fmt.Fprintf(out, "%-42s %s\n", r.Site, "READ FAILED: "+r.FailedWith.Error())
			continue
		}
		if r.total() == 0 {
			continue
		}
		fmt.Fprintf(out, "%-42s %8d %10d %6d %8d", r.Site,
			len(r.Reindex), len(r.Tombstone), len(r.Prune), len(r.Aliased))
		if r.Capped {
			fmt.Fprint(out, "  (capped)")
		}
		fmt.Fprintln(out)
		for _, id := range r.Aliased {
			fmt.Fprintf(out, "    ALIASED-MISSING %s\n", id)
		}
	}

	fmt.Fprintf(out, "\nTOTAL reindex=%d tombstone=%d prune=%d aliased-missing=%d read-failures=%d\n",
		reindex, tombstone, prune, aliased, failed)
	if aliased > 0 {
		fmt.Fprintf(out, "\naliased-missing is the dangerous class: an alias points at a deploy the index or R2 lost.\n")
	}
	if failed > 0 {
		return fmt.Errorf("drift report: %d site(s) could not be read", failed)
	}
	return nil
}
