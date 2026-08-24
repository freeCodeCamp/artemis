package main

import (
	"context"
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
	dirnames []string
	objects  map[string]bool
	listErr  error
	headErr  error
}

func (b orphanBucket) ListSites(context.Context) ([]string, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.dirnames, nil
}

func (b orphanBucket) HasObject(_ context.Context, key string) (bool, error) {
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
	return newReadOnlySweeper(base, staticLister{}, repo, reg, tmpl, bucket, []string{"production", "preview"})
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

func TestDriftSweep_OrphanAliasIgnoresAReservedName(t *testing.T) {
	t.Parallel()

	bucket := orphanBucket{
		dirnames: []string{"taken-down.freecode.camp"},
		objects:  map[string]bool{"taken-down.freecode.camp/preview": true},
	}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"taken-down.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "taken-down", State: registry.StateReserved}}}

	res, err := newOrphanSweeper(t, bucket, repo, reg).Run(context.Background())
	require.NoError(t, err)

	assert.Empty(t, res.OrphanAliases,
		"a reserved name is still owned; its grace window is not an orphan")
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
