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
)

func TestRelayBatch_ConcurrentReplicasAtLeastOncePublish(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	const total = 30
	for i := 0; i < total; i++ {
		require.NoError(t, repo.EnqueueSiteChanged(ctx, fmt.Sprintf("s%d", i)))
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
		assert.GreaterOrEqual(t, c, 1, "event %d published at least once across %d replicas (at-least-once; consumer worker.WorkflowGCSite is idempotent, E1)", id, replicas)
	}

	remaining, err := repo.FetchUnpublished(ctx, total)
	require.NoError(t, err)
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

	remaining, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 1, "the failed event stays unpublished for retry (at-least-once)")
	assert.Equal(t, "b", payloadSite(t, remaining[0]))
}

func payloadSite(t *testing.T, e OutboxEvent) string {
	t.Helper()
	var m map[string]string
	require.NoError(t, json.Unmarshal(e.Payload, &m))
	return m["site"]
}
