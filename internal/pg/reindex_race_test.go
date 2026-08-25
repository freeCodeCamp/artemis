package pg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func activeIDs(t *testing.T, repo *Repo, site sitekey.Dirname) map[string]struct{} {
	t.Helper()
	deploys, err := repo.DeploysForSite(context.Background(), site)
	require.NoError(t, err)
	ids := map[string]struct{}{}
	for _, d := range deploys {
		ids[d.ID] = struct{}{}
	}
	return ids
}

func tombstoneIDs(t *testing.T, repo *Repo, site sitekey.Dirname) map[string]struct{} {
	t.Helper()
	stones, err := repo.TombstonesForSite(context.Background(), site)
	require.NoError(t, err)
	ids := map[string]struct{}{}
	for _, s := range stones {
		ids[s.ID] = struct{}{}
	}
	return ids
}

func TestRepo_ConcurrentTombstoneAndReindexNeverBothWin(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	for round := range 25 {
		id := "d-race"
		_, err := repo.ClearTombstone(ctx, "www", id)
		require.NoError(t, err)
		require.NoError(t, repo.PruneDeploy(ctx, "www", id))
		require.NoError(t, repo.UpsertDeploy(ctx, "www", id, t0, 100, true, "active"))

		var wg sync.WaitGroup
		wg.Add(2)
		var tombErr, reindexErr error
		start := make(chan struct{})

		go func() {
			defer wg.Done()
			<-start
			tombErr = repo.WithSiteLock(ctx, "www", func(context.Context) error {
				return repo.RecordTombstone(ctx, "www", id, 100)
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			reindexErr = repo.WithSiteLock(ctx, "www", func(context.Context) error {
				_, err := repo.ReindexDeploy(ctx, "www", id, t0, true)
				return err
			})
		}()

		close(start)
		wg.Wait()
		require.NoError(t, tombErr, "round %d", round)
		require.NoError(t, reindexErr, "round %d", round)

		active := activeIDs(t, repo, "www")
		stones := tombstoneIDs(t, repo, "www")
		_, isActive := active[id]
		_, isTombstoned := stones[id]
		assert.False(t, isActive && isTombstoned,
			"round %d: a deploy must never be active and tombstoned at once; purge would delete bytes the index still points at", round)
		assert.True(t, isActive || isTombstoned,
			"round %d: one of the two writers must win; neither may lose the deploy entirely", round)
	}
}
