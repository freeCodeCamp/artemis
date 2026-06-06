package pg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithSiteLock_MutualExclusion(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	var mu sync.Mutex
	inside, maxInside := 0, 0

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := repo.WithSiteLock(ctx, "www.freecode.camp", func() error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				time.Sleep(30 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, maxInside, "advisory lock must serialize critical sections for one site")
}

func TestWithSiteLock_DistinctSitesDoNotBlock(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	release := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = repo.WithSiteLock(ctx, "a.freecode.camp", func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	done := make(chan struct{})
	go func() {
		_ = repo.WithSiteLock(ctx, "b.freecode.camp", func() error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lock on site b blocked by lock on site a")
	}
	close(release)
}

func TestAliasSupersede_StampsReleaseOnPreviousTarget(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, repo.FinalizeAtomic(ctx, "www.freecode.camp", "20260101-000000-aaaaaaa", "production", t0, 0))
	t1 := t0.Add(time.Minute)
	require.NoError(t, repo.FinalizeAtomic(ctx, "www.freecode.camp", "20260102-000000-bbbbbbb", "production", t1, 0))

	deploys, err := repo.DeploysForSite(ctx, "www.freecode.camp")
	require.NoError(t, err)
	byID := map[string]gc.Deploy{}
	for _, d := range deploys {
		byID[d.ID] = d
	}
	require.Len(t, byID, 2)
	assert.Equal(t, t1, byID["20260101-000000-aaaaaaa"].AliasReleasedAt.UTC(),
		"superseded deploy stamped at the moment it lost alias status (V11 bridge)")
	assert.True(t, byID["20260102-000000-bbbbbbb"].AliasReleasedAt.IsZero(),
		"current alias target carries no release stamp")
}
