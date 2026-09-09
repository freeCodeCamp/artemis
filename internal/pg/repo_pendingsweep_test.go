package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestRepo_PendingDeployIDs_SeesWhatDeploysForSiteHides(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	mustBegin(t, repo, ctx, "www", "abandoned", t0)
	mustBegin(t, repo, ctx, "www", "finished", t0)
	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "finished", "production", t0, 4096))
	mustBegin(t, repo, ctx, "learn", "elsewhere", t0)

	ids, err := repo.PendingDeployIDs(ctx, "www")
	require.NoError(t, err)

	assert.Equal(t, map[string]struct{}{"abandoned": {}}, ids,
		"reconcile classifies on absence from DeploysForSite, which filters state='active'; this is the "+
			"second reader that lets it tell a pending deploy from ownerless drift")
}

func TestRepo_PendingDeployIDs_EmptyForAnUnknownSite(t *testing.T) {
	repo := newTestRepo(t)

	ids, err := repo.PendingDeployIDs(context.Background(), "nosuchsite")
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestRepo_SitesWithExpiredPending_ReturnsOnlySitesPastTheCutoff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	mustBegin(t, repo, ctx, "latex", "old", t0.Add(-120*time.Hour))
	mustBegin(t, repo, ctx, "plumb-select", "older", t0.Add(-150*time.Hour))
	mustBegin(t, repo, ctx, "www", "fresh", t0)

	sites, err := repo.SitesWithExpiredPending(ctx, t0.Add(-72*time.Hour), 10)
	require.NoError(t, err)

	assert.Equal(t, []sitekey.Dirname{"latex", "plumb-select"}, sites,
		"the nightly sweep must visit exactly the sites holding an abandoned row, and a site still inside "+
			"the grace window is not one of them")
}

func TestRepo_SitesWithExpiredPending_IgnoresActiveRows(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	mustBegin(t, repo, ctx, "www", "d1", t0.Add(-120*time.Hour))
	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "d1", "production", t0.Add(-120*time.Hour), 4096))

	sites, err := repo.SitesWithExpiredPending(ctx, t0.Add(-72*time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, sites, "a finalized deploy is retention's problem, not the pending sweep's")
}

func TestRepo_SitesWithExpiredPending_DeduplicatesASiteWithSeveralRows(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	mustBegin(t, repo, ctx, "latex", "a", t0.Add(-120*time.Hour))
	mustBegin(t, repo, ctx, "latex", "b", t0.Add(-130*time.Hour))

	sites, err := repo.SitesWithExpiredPending(ctx, t0.Add(-72*time.Hour), 10)
	require.NoError(t, err)
	assert.Equal(t, []sitekey.Dirname{"latex"}, sites,
		"SweepPending collects every expired row for a site in one pass, so the site must appear once")
}

func TestRepo_SitesWithExpiredPending_BoundsTheReadNotOnlyTheWrite(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	for _, site := range []sitekey.Dirname{"a", "b", "c", "d"} {
		mustBegin(t, repo, ctx, site, "abandoned", t0.Add(-120*time.Hour))
	}

	sites, err := repo.SitesWithExpiredPending(ctx, t0.Add(-72*time.Hour), 2)
	require.NoError(t, err)

	assert.Equal(t, []sitekey.Dirname{"a", "b"}, sites,
		"the nightly ceiling must bound the query too; truncating in Go still materialises the whole backlog")
}
