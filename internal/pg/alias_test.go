package pg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlias_NoLostUpdate(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, repo.UpsertAlias(ctx, "www", "production", "A", now))

	var wg sync.WaitGroup
	type res struct {
		ok      bool
		current string
		err     error
	}
	results := make([]res, 2)
	nexts := []string{"B", "C"}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cur, ok, err := repo.SetAliasCAS(ctx, "www", "production", "A", nexts[i], now.Add(time.Minute))
			results[i] = res{ok, cur, err}
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, r := range results {
		require.NoError(t, r.err)
		if r.ok {
			wins++
		}
	}
	assert.Equal(t, 1, wins, "exactly one concurrent CAS from the same expected value wins (V8 no lost update)")

	targets, _, err := repo.AliasTargets(ctx, "www")
	require.NoError(t, err)
	assert.Len(t, targets, 1, "alias holds a single, consistent value")
	_, hasA := targets["A"]
	assert.False(t, hasA, "the stale value was overwritten by the winner")
}

func TestSetAliasCAS_DriftRejected(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertAlias(ctx, "www", "production", "A", now))

	cur, ok, err := repo.SetAliasCAS(ctx, "www", "production", "stale-expected", "Z", now)
	require.NoError(t, err)
	assert.False(t, ok, "CAS with wrong expected value is rejected")
	assert.Equal(t, "A", cur, "caller is told the actual current value")
}
