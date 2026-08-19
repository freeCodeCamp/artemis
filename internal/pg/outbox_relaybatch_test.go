package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestRelayBatch_ExclusiveAcrossReplicas(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	const total = 30
	for i := 0; i < total; i++ {
		require.NoError(t, repo.EnqueueSiteChanged(ctx, sitekey.Dirname(fmt.Sprintf("s%d", i))))
	}

	var mu sync.Mutex
	published := map[int64]int{}
	publish := func(e OutboxEvent) error {
		mu.Lock()
		published[e.ID]++
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		return nil
	}

	const replicas = 3
	var wg sync.WaitGroup
	errCh := make(chan error, replicas)
	for r := 0; r < replicas; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := repo.RelayBatch(ctx, 5, publish, time.Now())
				if err != nil {
					errCh <- err
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	require.Len(t, published, total, "every event claimed")
	for id, c := range published {
		assert.Equal(t, 1, c, "event %d must publish exactly once across %d replicas while its claim marker holds (B3); duplicates mean the claim is not exclusive", id, replicas)
	}

	remaining := fetchUnpublished(ctx, t, repo, total)
	assert.Empty(t, remaining, "every claimed event eventually marked published")
}

func TestRelayBatch_PublishFailureLeavesEventUnpublished(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "a"))
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "b"))

	boom := fmt.Errorf("engine down")
	calls := 0
	failing := func(OutboxEvent) error {
		calls++
		if calls == 1 {
			return nil
		}
		return boom
	}

	n, err := repo.RelayBatch(ctx, 10, failing, time.Now())
	require.Error(t, err)
	assert.Equal(t, 1, n, "only the pre-failure event marked")

	remaining := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, remaining, 1, "the failed event stays unpublished for retry (at-least-once)")
	assert.Equal(t, "b", payloadSite(t, remaining[0]))
}

func payloadSite(t *testing.T, e OutboxEvent) string {
	t.Helper()
	var m map[string]string
	require.NoError(t, json.Unmarshal(e.Payload, &m))
	return m["site"]
}

func TestRelayBatch_MarkSurvivesContextDeath(t *testing.T) {
	repo := newTestRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, s := range []sitekey.Dirname{"a", "b", "c"} {
		require.NoError(t, repo.EnqueueSiteChanged(context.Background(), s))
	}

	calls := 0
	publish := func(OutboxEvent) error {
		calls++
		if calls == 1 {
			return nil
		}
		cancel()
		return fmt.Errorf("publish: %w", context.Canceled)
	}

	n, err := repo.RelayBatch(ctx, 10, publish, time.Now())
	require.Error(t, err, "the publish failure must surface, not be swallowed by the mark")
	assert.Equal(t, 1, n, "the pre-failure publish must be marked even though the batch ctx died")

	remaining := fetchUnpublished(context.Background(), t, repo, 10)
	assert.Len(t, remaining, 2, "only the published event may be marked; the rest stay for retry")
}

func TestClaimTTL_ExceedsBatchAndMarkBudget(t *testing.T) {
	require.Greater(t, claimTTL, 2*time.Minute+10*time.Second,
		"claimTTL must exceed worker.DefaultRelayBatchTimeout (2m) plus the 10s detached mark window, or an expiring claim re-publishes the rows it protects; worker cannot be imported here (cycle), so the bound is pinned literally")
}

func TestClaimBatch_ExpiredClaimIsReclaimable(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, s := range []sitekey.Dirname{"a", "b"} {
		require.NoError(t, repo.EnqueueSiteChanged(ctx, s))
	}

	first, err := repo.claimBatch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, first, 2)

	second, err := repo.claimBatch(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, second, "a live claim must be exclusive")

	_, err = repo.pool.Exec(ctx, `UPDATE outbox SET claim_expires_at = now() - interval '1 second'`)
	require.NoError(t, err)

	third, err := repo.claimBatch(ctx, 10)
	require.NoError(t, err)
	require.Len(t, third, 2, "an expired claim is the crash-recovery path; its rows must become claimable again")
}
