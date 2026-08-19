package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/registry"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestReadOnlyStore_RefusesEveryWrite(t *testing.T) {
	t.Parallel()

	s := readOnlyStore{inner: nopReconcileStore{}}
	ctx := context.Background()

	_, err := s.ReindexDeploy(ctx, "www", "d1", time.Now(), true)
	require.ErrorIs(t, err, errReadOnlyViolation)
	require.ErrorIs(t, s.RecordTombstone(ctx, "www", "d1", 0), errReadOnlyViolation)
	require.ErrorIs(t, s.PruneDeploy(ctx, "www", "d1"), errReadOnlyViolation)

	_, err = readOnlyMover{}.MovePrefix(ctx, "a/", "b/")
	require.ErrorIs(t, err, errReadOnlyViolation)

	deploys, err := s.DeploysForSite(ctx, "www")
	require.NoError(t, err, "reads still pass through; only writes are refused")
	require.Empty(t, deploys)
}

type failingDirnameReader struct{ err error }

func (r failingDirnameReader) KnownSiteDirnames(context.Context) ([]sitekey.Dirname, error) {
	return nil, r.err
}

type failingRegistryReader struct{ err error }

func (r failingRegistryReader) Sites(context.Context) ([]registry.Site, error) { return nil, r.err }

func TestDriftReportSites_PropagatesEitherReaderFailure(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	boom := errors.New("pg down")

	_, err = driftReportSites(context.Background(),
		failingDirnameReader{err: boom}, fakeRegistryReader{}, tmpl)
	require.ErrorIs(t, err, boom, "a half-read site list would silently narrow the sweep")

	_, err = driftReportSites(context.Background(),
		fakeDirnameReader{}, failingRegistryReader{err: boom}, tmpl)
	require.ErrorIs(t, err, boom)
}

type driftInnerStore struct{ nopReconcileStore }

func (driftInnerStore) DeploysForSite(context.Context, sitekey.Dirname) ([]gc.Deploy, error) {
	return []gc.Deploy{{ID: "ghost", Mtime: time.Now().Add(-30 * 24 * time.Hour)}}, nil
}

func driftReportReconciler(store gc.ReconcileStore) *gc.Reconciler {
	orphan := time.Now().Add(-30*24*time.Hour).UTC().Format("20060102-150405") + "-sha1234"
	return &gc.Reconciler{
		Lister:       staticLister{keys: []string{"www/deploys/" + orphan + "/index.html"}},
		Store:        store,
		Mover:        readOnlyMover{},
		Grace:        time.Hour,
		SitePrefix:   func(s sitekey.Dirname) string { return string(s) + "/deploys/" },
		DeployPrefix: func(s sitekey.Dirname, id string) string { return string(s) + "/deploys/" + id + "/" },
		TrashPrefix:  func(s sitekey.Dirname, id string) string { return "_trash/" + string(s) + "/" + id + "/" },
		LiveAliases: func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		},
		Now: time.Now,
	}
}

func TestDriftReportWiring_DryRunFindsDriftAndWritesNothing(t *testing.T) {
	t.Parallel()

	rc := driftReportReconciler(readOnlyStore{inner: driftInnerStore{}})

	report, err := rc.ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err,
		"a dry run must never reach a write, so the read-only guards must stay silent")

	assert.NotEmpty(t, report.OrphanTombstoned, "the report still names what a live run would repair")
	assert.NotEmpty(t, report.PGPruned)
}

func TestDriftReportWiring_LiveRunIsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	rc := driftReportReconciler(readOnlyStore{inner: driftInnerStore{}})
	rc.Locker = nil

	_, err := rc.ReconcileSite(context.Background(), "www", false)

	require.Error(t, err, "the report binary has no Locker, so a live run cannot start")
	require.NotErrorIs(t, err, errReadOnlyViolation,
		"it must refuse at wiring, before a mutation is even attempted")
}

type fakeDirnameReader struct{ sites []sitekey.Dirname }

func (r fakeDirnameReader) KnownSiteDirnames(context.Context) ([]sitekey.Dirname, error) {
	return r.sites, nil
}

type fakeRegistryReader struct{ slugs []sitekey.Slug }

func (r fakeRegistryReader) Sites(context.Context) ([]registry.Site, error) {
	out := make([]registry.Site, 0, len(r.slugs))
	for _, s := range r.slugs {
		out = append(out, registry.Site{Slug: s})
	}
	return out, nil
}

func TestDriftReportSites_CoversSitesTheSchedulerCannotSee(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)

	sites, err := driftReportSites(context.Background(),
		fakeDirnameReader{sites: []sitekey.Dirname{"orphan.freecode.camp", "www.freecode.camp"}},
		fakeRegistryReader{slugs: []sitekey.Slug{"www", "quiet"}},
		tmpl)
	require.NoError(t, err)

	assert.Equal(t, []sitekey.Dirname{"orphan.freecode.camp", "quiet.freecode.camp", "www.freecode.camp"}, sites,
		"the sweep is the union: registry slugs the cron uses, plus dirnames only the stores know about")
}

func TestWriteDriftReport_SurfacesTotalsAndHidesCleanSites(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := writeDriftReport(&buf, []siteDrift{
		{Site: "clean.freecode.camp"},
		{Site: "dirty.freecode.camp", Tombstone: []string{"d1", "d2"}, Prune: []string{"d3"}},
		{Site: "danger.freecode.camp", Aliased: []string{"d9"}},
	}, config.CleanupConfig{Grace: 72 * time.Hour, BlastCap: 10})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "TOTAL reindex=0 tombstone=2 prune=1 aliased-missing=1 read-failures=0")
	assert.Contains(t, out, "ALIASED-MISSING d9")
	assert.NotContains(t, out, "clean.freecode.camp", "a site with no drift is noise in a sweep of 70")
	assert.Contains(t, out, "read-only, no writes possible")
}

func TestWriteDriftReport_FailsWhenASiteCouldNotBeRead(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := writeDriftReport(&buf, []siteDrift{
		{Site: "broken.freecode.camp", FailedWith: context.DeadlineExceeded},
	}, config.CleanupConfig{})

	require.Error(t, err, "an unread site is unknown drift, not zero drift")
	assert.True(t, strings.Contains(buf.String(), "READ FAILED"))
}

func TestSweepStats_RejectsASweepThatTouchedNothing(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, sweepStats{}.validate(), "swept 0 sites")
}

func TestSweepStats_RejectsTheKeyspaceBugSignature(t *testing.T) {
	t.Parallel()

	err := sweepStats{Sites: 70, IndexedTotal: 198, PGDeploys: 198, R2Objects: 0}.validate()

	require.ErrorContains(t, err, "misconfiguration, not zero drift",
		"a sweep that lists nothing anywhere is exactly what the silent keyspace bug looked like")
	require.ErrorContains(t, err, "198 deploys across 70 sites")
}

func TestSweepStats_RefusesToPlanAgainstMostOfThePostgresIndex(t *testing.T) {
	t.Parallel()

	err := sweepStats{Sites: 70, IndexedTotal: 198, PGDeploys: 198, R2Objects: 4, Prunes: 150}.validate()

	require.ErrorContains(t, err, "refusing to report a sweep that disagrees with more than half")
}

func TestSweepStats_AcceptsAPlausibleSweep(t *testing.T) {
	t.Parallel()

	require.NoError(t, sweepStats{Sites: 70, IndexedTotal: 198, PGDeploys: 198, R2Objects: 4212, Prunes: 3}.validate())
	require.NoError(t, sweepStats{Sites: 70, IndexedTotal: 0, PGDeploys: 0, R2Objects: 0}.validate(),
		"an empty platform is legitimately empty, not a misconfiguration")
}

func TestCountingLayers_TallyWhatTheSweepActuallyRead(t *testing.T) {
	t.Parallel()

	lister := &countingLister{inner: staticLister{keys: []string{"a", "b", "c"}}}
	keys, err := lister.ListPrefix(context.Background(), "www/deploys/")
	require.NoError(t, err)
	require.Len(t, keys, 3)
	assert.Equal(t, 3, lister.objects)

	store := &countingStore{ReconcileStore: driftInnerStore{}}
	deploys, err := store.DeploysForSite(context.Background(), "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1)
	assert.Equal(t, 1, store.deploys)
}

func TestSweepStats_CatchesASweepWhoseSiteListMatchesNothing(t *testing.T) {
	t.Parallel()

	err := sweepStats{Sites: 70, IndexedTotal: 198, PGDeploys: 0, R2Objects: 0}.validate()

	require.ErrorContains(t, err, "the site list is wrong",
		"this is the original keyspace bug: 70 sites swept under names no store uses, so every per-site read came back empty")
	require.ErrorContains(t, err, "198 active rows")
}

func TestFinishReport_KeepsBothTheSelfCheckAndTheReadFailure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := finishReport(&buf,
		[]siteDrift{{Site: "broken.freecode.camp", FailedWith: context.DeadlineExceeded}},
		config.CleanupConfig{},
		sweepStats{Sites: 0})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "swept 0 sites",
		"the self-check verdict must survive")
	assert.Contains(t, err.Error(), "could not be read",
		"and it must not swallow the read failure that may well explain it")
}

func TestFinishReport_AReadFailureDoesNotGetBlamedOnTheSiteList(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := finishReport(&buf,
		[]siteDrift{{Site: "www.freecode.camp", FailedWith: context.DeadlineExceeded}},
		config.CleanupConfig{},
		sweepStats{Sites: 1, IndexedTotal: 198, PGDeploys: 0, R2Objects: 0})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "the site list is wrong",
		"R2 being down stops the per-site reads, which is not evidence about the site list")
	assert.Contains(t, err.Error(), "could not be read")
	assert.Contains(t, buf.String(), "read-failures=1")
}
