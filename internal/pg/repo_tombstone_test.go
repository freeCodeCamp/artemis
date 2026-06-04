package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepo_RecordTombstone(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys, "recorded tombstone removes the deploy from the active set")

	expired, err := repo.ExpiredTombstones(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "d1", expired[0].ID)
	assert.EqualValues(t, 100, expired[0].Bytes)
}

func TestRepo_RecordTombstone_Idempotent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 100, true, "active"))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100))
	require.NoError(t, repo.RecordTombstone(ctx, "www", "d1", 100),
		"second RecordTombstone for same id is a no-op (ON CONFLICT DO NOTHING)")

	expired, err := repo.ExpiredTombstones(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, expired, 1, "still exactly one tombstone after the repeat")
}

func TestRepo_PruneDeploy(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0, 1, false, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "learn", "d1", t0, 1, false, "active"))

	require.NoError(t, repo.PruneDeploy(ctx, "www", "d1"))

	w, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, w, "the named site's deploy is pruned")

	l, err := repo.DeploysForSite(ctx, "learn")
	require.NoError(t, err)
	assert.Len(t, l, 1, "a same-id deploy on a different site is left intact")
}

func TestRepo_PruneDeploy_MissingRow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.PruneDeploy(ctx, "www", "absent"),
		"pruning a non-existent deploy row is idempotent (no error)")
}
