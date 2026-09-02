package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type orphanBucket struct {
	dirnames    []string
	objects     map[string]bool
	listErr     error
	headErr     error
	headErrKeys map[string]error
}

func (b orphanBucket) ListSites(context.Context) ([]string, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.dirnames, nil
}

func (b orphanBucket) HasObject(_ context.Context, key string) (bool, error) {
	if b.headErrKeys[key] != nil {
		return false, b.headErrKeys[key]
	}
	if b.headErr != nil {
		return false, b.headErr
	}
	return b.objects[key], nil
}

type orphanRepo struct {
	nopReconcileStore
	dirnames []sitekey.Dirname
}

func (r orphanRepo) KnownSiteDirnames(context.Context) ([]sitekey.Dirname, error) {
	return r.dirnames, nil
}

func (orphanRepo) CountDeploys(context.Context) (int, error) { return 0, nil }

type statefulRegistryReader struct{ sites []registry.Site }

func (r statefulRegistryReader) Sites(context.Context) ([]registry.Site, error) {
	return r.sites, nil
}

func newOrphanSweeper(t *testing.T, bucket bucketAliasReader, repo driftSweepRepo, reg registrySiteReader) *driftSweep {
	t.Helper()
	tmpl, err := handler.NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	base := &gc.Reconciler{
		Lister:       staticLister{},
		Store:        nopReconcileStore{},
		Grace:        time.Hour,
		SitePrefix:   func(s sitekey.Dirname) string { return string(s) + "/deploys/" },
		DeployPrefix: func(s sitekey.Dirname, id string) string { return string(s) + "/deploys/" + id + "/" },
		TrashPrefix:  func(s sitekey.Dirname, id string) string { return "_trash/" + string(s) + "/" + id + "/" },
		LiveAliases: func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		},
		Now: time.Now,
	}
	return newReadOnlySweeper(base, staticLister{}, repo, reg, tmpl, bucket, []string{"production", "preview"}, nil)
}

func TestDriftSweep_OrphanAliasReportsAnUnregisteredAliasKey(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"ghost.freecode.camp", "www.freecode.camp"},
		objects: map[string]bool{
			"ghost.freecode.camp/production": true,
			"www.freecode.camp/production":   true,
		},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"ghost.freecode.camp", "www.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "www", State: registry.StateActive}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []orphanAlias{{Dirname: "ghost.freecode.camp", Modes: []string{"production"}}}, res.OrphanAliases,
		"an alias with no registry row is a site serving a stranger's content off a name nobody owns")
}

func TestDriftSweep_OrphanAliasIgnoresARegisteredSite(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"www.freecode.camp"},
		objects: map[string]bool{
			"www.freecode.camp/production": true,
			"www.freecode.camp/preview":    true,
		},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"www.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "www", State: registry.StateActive}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Empty(t, res.OrphanAliases, "every live site would page nightly")
}

func TestDriftSweep_OrphanAliasIgnoresAReservedNameThatServesNothing(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"taken-down.freecode.camp"},
		objects:  map[string]bool{},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"taken-down.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "taken-down", State: registry.StateReserved}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Empty(t, res.OrphanAliases,
		"the reserving delete removes both alias objects before it flips the state, so every held name would page nightly")
}

func TestDriftSweep_OrphanAliasReportsAReservedNameThatIsStillServing(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"taken-down.freecode.camp"},
		objects:  map[string]bool{"taken-down.freecode.camp/production": true},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"taken-down.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "taken-down", State: registry.StateReserved}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []orphanAlias{{Dirname: "taken-down.freecode.camp", Modes: []string{"production"}}}, res.OrphanAliases,
		"a reserved name serving content is a site the sweep will trash within its grace; counting the held row as registered hides it until the bytes are already gone")
}

func TestDriftSweep_OrphanAliasSkipsTheTrashPrefix(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"_trash", "www.freecode.camp"},
		objects:  map[string]bool{"_trash/production": true},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"www.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "www", State: registry.StateActive}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Empty(t, res.OrphanAliases,
		"artemis-owned prefixes are never site dirnames, whatever the bucket lister returns")
}

func TestDriftSweep_OrphanAliasPhaseIsSkippedForAScopedSweep(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"ghost.freecode.camp"},
		objects:  map[string]bool{"ghost.freecode.camp/production": true},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"ghost.freecode.camp"}}
	reg := statefulRegistryReader{}

	res, err := newOrphanSweeper(t, bucket, repo, reg).runSite(context.Background(), "www.freecode.camp")
	require.NoError(t, err)

	assert.Empty(t, res.OrphanAliases,
		"one site's reconcile must not report the whole bucket's orphans")
}

func TestDriftSweep_OrphanAliasKeepsWhatItFoundWhenOneHeadFails(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"ghost.freecode.camp", "flaky.freecode.camp"},
		objects:  map[string]bool{"ghost.freecode.camp/production": true},
		headErrKeys: map[string]error{
			"flaky.freecode.camp/production": errors.New("r2 head timeout"),
		},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"ghost.freecode.camp", "flaky.freecode.camp"}}
	reg := statefulRegistryReader{}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err, "a partial orphan scan is degraded, not a failed sweep")

	assert.Equal(t, []orphanAlias{{Dirname: "ghost.freecode.camp", Modes: []string{"production"}}}, res.OrphanAliases,
		"one flaky HEAD must not hide an orphan the scan already proved")
	require.Error(t, res.OrphanErr,
		"the unscanned name is unknown, not clean, and the verdict must say so")
}
