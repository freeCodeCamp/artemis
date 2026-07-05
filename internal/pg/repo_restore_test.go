package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

func TestRepo_RestoreDeploy_Roundtrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))

	tombstones, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, tombstones, 1)
	assert.Equal(t, "d1", tombstones[0].ID)
	assert.EqualValues(t, 100, tombstones[0].Bytes)

	restoreAt := t0.Add(time.Hour)
	require.NoError(t, repo.RestoreDeploy(ctx, "www", "d1", restoreAt, 250))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1, "restored deploy reappears in the active set")
	assert.Equal(t, "d1", deploys[0].ID)
	assert.WithinDuration(t, restoreAt, deploys[0].Mtime, time.Second)
	assert.EqualValues(t, 250, deploys[0].Bytes, "restore persists the caller-supplied (real R2) byte count")

	tombstones, err = repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, tombstones, "restored tombstone is gone")
}

func TestRepo_RestoreDeploy_NotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	err := repo.RestoreDeploy(ctx, "www", "never-tombstoned", time.Now().UTC(), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, registry.ErrNotFound),
		"restoring a non-tombstoned id surfaces registry.ErrNotFound")
}

func TestRepo_TombstonesForSite_MultipleSites(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 10, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "learn", "d2", t0, 20, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 10))
	require.NoError(t, repo.RecordTombstone(ctx, "learn", "d2", 20))

	wwwTombstones, err := repo.TombstonesForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, wwwTombstones, 1)
	assert.Equal(t, "d1", wwwTombstones[0].ID)

	learnTombstones, err := repo.TombstonesForSite(ctx, "learn")
	require.NoError(t, err)
	require.Len(t, learnTombstones, 1)
	assert.Equal(t, "d2", learnTombstones[0].ID)
}
