package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepo_UpsertDeploy_ResurrectsATombstonedDeploy(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 0, true, "active"))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, deploys, 1,
		"UpsertDeploy is the unguarded finalize path: it re-activates a tombstoned deploy by design")

	expired, err := repo.ExpiredTombstones(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, expired, 1,
		"the tombstone survives, so purge will hard-delete bytes an active row still points at")
}

func TestRepo_ReindexDeploy_RefusesToResurrectATombstonedDeploy(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0, true)
	require.NoError(t, err)
	assert.False(t, reindexed, "a tombstoned deploy must report no reindex")

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys, "the tombstoned deploy stays out of the active set")
}

func TestRepo_ReindexDeploy_RefusesToResurrectATombstonedSite(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordSiteTombstone(ctx, "www"))

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0, true)
	require.NoError(t, err)
	assert.False(t, reindexed,
		"a site tombstone records itself as (site, '') and deletes every deploy row, so a per-deploy guard "+
			"matches nothing; bytes that outlive MovePrefix would flip the tombstoned site back to active")

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys, "the tombstoned site stays out of the active set")
}

func TestRepo_ReindexDeploy_ResumesAfterTheSiteTombstoneClears(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.RecordSiteTombstone(ctx, "www"))
	cleared, err := repo.ClearTombstone(ctx, "www", "")
	require.NoError(t, err)
	require.True(t, cleared)

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0, true)
	require.NoError(t, err)
	assert.True(t, reindexed,
		"TombstonePurge clears the site tombstone once the recovery window closes, so the refusal is "+
			"bounded by that window and does not disable reconcile for the site forever")
}

func TestRepo_ReindexDeploy_InsertsAnUnknownDeploy(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0, true)
	require.NoError(t, err)
	assert.True(t, reindexed)

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1)
	assert.Equal(t, "d1", deploys[0].ID)
	assert.True(t, deploys[0].HasMarker)
	assert.WithinDuration(t, t0, deploys[0].Mtime, time.Second)
}

func TestRepo_ReindexDeploy_KeepsKnownBytesOnRepeat(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 4096, true, "active"))

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0.Add(time.Minute), true)
	require.NoError(t, err)
	assert.True(t, reindexed)

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1)
	assert.EqualValues(t, 4096, deploys[0].Bytes,
		"reconcile knows no byte count and must not clobber the finalize path's value")
	assert.WithinDuration(t, t0, deploys[0].Mtime, time.Second,
		"reconcile derives mtime from the deploy id; rewinding a finalize-written mtime ages the deploy toward early GC")
}

func TestRepo_ReindexDeploy_RestoreClearsTheRefusal(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))
	require.NoError(t, repo.RestoreDeploy(ctx, "www", "d1", t0, 100))

	reindexed, err := repo.ReindexDeploy(ctx, "www", "d1", t0, true)
	require.NoError(t, err)
	assert.True(t, reindexed, "restore drops the tombstone, so reindex is allowed again")
}

func TestRepo_ClearTombstone_SitePurgeClearsEveryTombstoneItsDeleteRemoved(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 10))
	require.NoError(t, repo.RecordSiteTombstone(ctx, "www"))

	cleared, err := repo.ClearTombstone(ctx, "www", "")
	require.NoError(t, err)
	require.True(t, cleared)

	left, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, left,
		"the site purge hard-deletes all of _trash/<site>/, so a surviving per-deploy row points at objects that no longer exist and makes restore answer 200 for a deploy with no bytes")
}

func TestRepo_ClearTombstone_ADeployPurgeLeavesItsSiblingsAlone(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 10))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d2", 20))

	cleared, err := repo.ClearTombstone(ctx, "www", "d1")
	require.NoError(t, err)
	require.True(t, cleared)

	left, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, left, 1, "a per-deploy purge deletes one prefix, so it may clear only that one row")
	assert.Equal(t, "d2", left[0].ID)
}
