package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeploySaga(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	mtime := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "20260420-141522-abc1234", "production", mtime, 4096))

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	require.Len(t, deploys, 1)
	assert.True(t, deploys[0].HasMarker, "finalize marks deploy completed")
	assert.EqualValues(t, 4096, deploys[0].Bytes)

	targets, _, err := repo.AliasTargets(ctx, "www")
	require.NoError(t, err)
	assert.Contains(t, targets, "20260420-141522-abc1234", "production alias points at the finalized deploy")

	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "exactly one site.changed emitted in the same tx")
	assert.Equal(t, TopicSiteChanged, events[0].Topic)

	require.NoError(t, repo.FinalizeAtomic(ctx, "www", "20260420-141522-abc1234", "production", mtime, 4096),
		"re-finalize is idempotent on (site,id) and (site,name)")
	deploys, err = repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, deploys, 1, "no duplicate deploy row")
}
