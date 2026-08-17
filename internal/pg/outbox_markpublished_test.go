package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkPublished_EmptyBatchMarksNothing(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, repo.MarkPublished(ctx, nil, now), "nil id slice is a guarded no-op")
	require.NoError(t, repo.MarkPublished(ctx, []int64{}, now), "empty id slice is a guarded no-op")

	require.NoError(t, repo.EnqueueSiteChanged(ctx, "www"))
	events := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, events, 1)

	require.NoError(t, repo.MarkPublished(ctx, nil, now),
		"a nil batch must not touch existing unpublished rows")
	still := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, still, 1, "the empty-batch no-op left the real event unpublished")

	ids := []int64{events[0].ID}
	require.NoError(t, repo.MarkPublished(ctx, ids, now))
	after := fetchUnpublished(ctx, t, repo, 10)
	assert.Empty(t, after, "a non-empty batch marks the event published")
}
