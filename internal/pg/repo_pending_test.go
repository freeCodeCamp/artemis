package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepo_BeginDeploy_IsInvisibleToEveryActiveRead(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.BeginDeploy(ctx, "www", "d1", t0))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Empty(t, deploys,
		"a deploy that has only been initialised holds no finished bytes, so retention planning, the drift "+
			"denominator and the API must all keep ignoring it")

	n, err := repo.CountDeploys(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRepo_BeginDeploy_IsPromotedByFinalize(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.BeginDeploy(ctx, "www", "d1", t0))
	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "d1", "production", t0, 4096))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1,
		"finalize already upserts ON CONFLICT ... SET state = 'active', so the pending row it lands on must "+
			"become the ordinary active deploy with no extra write")
	assert.EqualValues(t, 4096, deploys[0].Bytes)

	pending, err := repo.ExpiredPendingDeploys(ctx, "www", t0.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, pending, "a finalized deploy must never be reaped as an abandoned one")
}

func TestRepo_BeginDeploy_IsIdempotentAndNeverDemotesALiveDeploy(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.BeginDeploy(ctx, "www", "d1", t0))
	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "d1", "production", t0, 4096))
	require.NoError(t, repo.BeginDeploy(ctx, "www", "d1", t0))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1,
		"a retried init against a finalized id must not flip a serving deploy back to pending, which would "+
			"queue the live site's own bytes for expiry")
	assert.EqualValues(t, 4096, deploys[0].Bytes)
}

func TestRepo_ExpiredPendingDeploys_ReturnsOnlyRowsPastTheCutoff(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.BeginDeploy(ctx, "www", "old", t0.Add(-96*time.Hour)))
	require.NoError(t, repo.BeginDeploy(ctx, "www", "fresh", t0))
	require.NoError(t, repo.BeginDeploy(ctx, "learn", "other", t0.Add(-96*time.Hour)))

	got, err := repo.ExpiredPendingDeploys(ctx, "www", t0.Add(-72*time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 1,
		"the cutoff must sit far above the deploy JWT TTL so an upload still in flight is never reaped")
	assert.Equal(t, "old", got[0].ID)
}

func TestRepo_ExpiredPendingDeploys_IgnoresActiveRows(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.UpsertDeploy(ctx, "www", "d1", t0.Add(-96*time.Hour), 100, true, "active"))

	got, err := repo.ExpiredPendingDeploys(ctx, "www", t0.Add(-72*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, got,
		"expiry reaps abandoned sessions only; an old active deploy is retention's business, not this query's")
}
