package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepo_OutboxBacklog_IsZeroWhenNothingIsWaiting(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	count, oldest, err := repo.OutboxBacklog(ctx)

	require.NoError(t, err)
	assert.Zero(t, count)
	assert.Zero(t, oldest)
}

func TestRepo_OutboxBacklog_AgesTheOldestUnpublishedEvent(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "www.freecode.camp"))
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "learn.freecode.camp"))
	_, err := repo.pool.Exec(ctx,
		`UPDATE outbox SET created_at = now() - interval '20 minutes' WHERE id = (SELECT min(id) FROM outbox)`)
	require.NoError(t, err)

	count, oldest, err := repo.OutboxBacklog(ctx)

	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Greater(t, oldest, 19*time.Minute,
		"the alert turns on the age of the head of the queue, so a stalled relay is visible "+
			"even when only one event is stuck behind it")
}

func TestRepo_OutboxBacklog_IgnoresPublishedEvents(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "www.freecode.camp"))
	drained, err := repo.RelayBatch(ctx, 10, func(OutboxEvent) error { return nil }, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 1, drained)

	count, oldest, err := repo.OutboxBacklog(ctx)

	require.NoError(t, err)
	assert.Zero(t, count, "a published row is history, not backlog; counting it would alert forever")
	assert.Zero(t, oldest)
}
