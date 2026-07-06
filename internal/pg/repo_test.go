package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/freeCodeCamp/artemis/internal/gc"
)

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("artemis_test"),
		postgres.WithUsername("artemis"),
		postgres.WithPassword("artemis"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := New(ctx, Config{DatabaseURL: connStr})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	require.NoError(t, Migrate(ctx, db.Pool))
	return NewRepo(db)
}

func TestRepo_DeployAliasRoundtrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now.Add(-time.Hour), 100, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d2", now.Add(-2*time.Hour), 200, false, "active"))
	require.NoError(t, repo.UpsertAlias(ctx, "www", "production", "d1", now))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, deploys, 2)

	targets, last, err := repo.AliasTargets(ctx, "www")
	require.NoError(t, err)
	assert.Contains(t, targets, "d1")
	assert.WithinDuration(t, now, last, time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now, 150, true, "active"),
		"upsert is idempotent on (site,id)")
	deploys, err = repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, deploys, 2, "re-upsert updates in place, no duplicate row")
}

func TestRepo_UpsertDeploy_ZeroBytesDoesNotClobber(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now, 300, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now, 0, true, "active"),
		"a later soft-fail re-upserts with bytes=0")

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1)
	assert.EqualValues(t, 300, deploys[0].Bytes,
		"bytes=0 upsert must NOT clobber a known-good nonzero value")

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now, 500, true, "active"))
	deploys, err = repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.EqualValues(t, 500, deploys[0].Bytes, "a real nonzero bytes still updates in place")
}

func TestRepo_AliasAtomicStampsSupersededRelease(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", now.Add(-2*time.Hour), 100, true, "active"))
	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d2", now.Add(-time.Hour), 200, true, "active"))
	require.NoError(t, repo.UpsertAlias(ctx, "www", "production", "d1", now.Add(-time.Hour)))

	release := now
	require.NoError(t, repo.AliasAtomic(ctx, "www", "production", "d2", release))

	byID := map[string]gc.Deploy{}
	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	for _, d := range deploys {
		byID[d.ID] = d
	}
	assert.WithinDuration(t, release, byID["d1"].AliasReleasedAt, time.Second,
		"deploy losing the production alias is stamped alias_released_at in the same tx")
	assert.True(t, byID["d2"].AliasReleasedAt.IsZero(),
		"newly-aliased deploy carries no release stamp")

	targets, _, err := repo.AliasTargets(ctx, "www")
	require.NoError(t, err)
	assert.Contains(t, targets, "d2", "alias moved to d2")
}

func TestRepo_TombstoneLifecycle(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d-old", now.Add(-30*24*time.Hour), 100, true, "active"))
	require.NoError(t, repo.Tombstone(ctx, "www", gc.Deploy{ID: "d-old", Bytes: 100}))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys, "tombstoned deploy removed from active set")

	expired, err := repo.ExpiredTombstones(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "d-old", expired[0].ID)
	assert.EqualValues(t, 100, expired[0].Bytes)

	none, err := repo.ExpiredTombstones(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, none, "tombstone trashed_at in the future of the cutoff is not yet expired")

	cleared, clearErr := repo.ClearTombstone(ctx, "www", "d-old")
	require.NoError(t, clearErr)
	require.True(t, cleared, "an existing tombstone row is reported cleared")
	expired, err = repo.ExpiredTombstones(ctx, now.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, expired, "cleared tombstone gone")
}
