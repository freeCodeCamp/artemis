package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepo_RecordSitePurge_ClearsTheWholeSiteIndex(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d2", t0, 200, true, "active"))
	require.NoError(t, repo.UpsertAlias(ctx, "www", "production", "d1", t0))
	require.NoError(t, repo.UpsertDeploy(ctx, "learn", "d1", t0, 50, true, "active"))
	require.NoError(t, repo.UpsertAlias(ctx, "learn", "production", "d1", t0))

	require.NoError(t, repo.RecordSitePurge(ctx, "www"))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys,
		"a purge moves every byte, so leaving index rows behind describes bytes that no longer exist")

	targets, _, err := repo.AliasTargets(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, targets,
		"an alias pointing into a purged site is the drift class that pages; the purge must not create it")

	stones, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, stones, 1)
	assert.Empty(t, stones[0].ID, "the site-level tombstone records the purge itself")

	otherDeploys, err := repo.DeploysForSite(ctx, "learn")
	require.NoError(t, err)
	assert.Len(t, otherDeploys, 1, "another site is untouched")
	otherTargets, _, err := repo.AliasTargets(ctx, "learn")
	require.NoError(t, err)
	assert.Len(t, otherTargets, 1)
}

func TestRepo_RecordSitePurge_IsIdempotent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordSitePurge(ctx, "www"))
	require.NoError(t, repo.RecordSitePurge(ctx, "www"),
		"a retried purge must not fail on its own tombstone row")

	stones, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, stones, 1)
}

func TestRepo_RecordSitePurge_RestartsTheRecoveryWindow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordSitePurge(ctx, "www"))
	first, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, first, 1)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", time.Now().UTC(), 100, true, "active"))
	require.NoError(t, repo.RecordSitePurge(ctx, "www"))

	second, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.True(t, second[0].TrashedAt.After(first[0].TrashedAt),
		"the site tombstone dates the whole _trash/<site>/ prefix, so a later purge that inherits an older "+
			"trashed_at gives its bytes a short recovery window and TombstonePurge hard-deletes them early")
}

func TestRepo_KnownSiteDirnames_UnionsEveryTableThatNamesASite(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	names, err := repo.KnownSiteDirnames(ctx)
	require.NoError(t, err)
	assert.Empty(t, names)

	require.NoError(t, repo.UpsertDeploy(ctx, "www.freecode.camp", "d1", t0, 1, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "www.freecode.camp", "d2", t0, 1, true, "active"))
	require.NoError(t, repo.UpsertAlias(ctx, "learn.freecode.camp", "production", "d9", t0))
	require.NoError(t, repo.RecordSitePurge(ctx, "gone.freecode.camp"))

	names, err = repo.KnownSiteDirnames(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"gone.freecode.camp", "learn.freecode.camp", "www.freecode.camp"}, names,
		"the sweep enumerates from this list, so a site named only by an alias or only by a tombstone "+
			"is a site no sweep would otherwise look at")
}

func TestRepo_CountDeploys_CountsOnlyActiveRows(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	n, err := repo.CountDeploys(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 1, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "learn", "d1", t0, 1, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "learn", "d2", t0, 1, true, "retired"))

	n, err = repo.CountDeploys(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n,
		"the drift sweep's independent denominator must match what DeploysForSite would return")
}
