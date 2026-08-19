package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedOutbox(ctx context.Context, t *testing.T, repo *Repo, topic string, published *time.Time) int64 {
	t.Helper()
	var id int64
	require.NoError(t, repo.pool.QueryRow(ctx,
		`INSERT INTO outbox (topic, payload, published_at) VALUES ($1, '{}'::jsonb, $2) RETURNING id`,
		topic, published).Scan(&id))
	return id
}

func outboxIDs(ctx context.Context, t *testing.T, repo *Repo) map[int64]struct{} {
	t.Helper()
	rows, err := repo.pool.Query(ctx, `SELECT id FROM outbox`)
	require.NoError(t, err)
	defer rows.Close()

	out := map[int64]struct{}{}
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		out[id] = struct{}{}
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPurgeOutbox_DropsPublishedRowsPastTheWindow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)

	stale := seedOutbox(ctx, t, repo, "site.changed", &old)
	fresh := seedOutbox(ctx, t, repo, "site.changed", &recent)
	undelivered := seedOutbox(ctx, t, repo, "site.changed", nil)

	n, err := repo.PurgeOutbox(ctx, now.Add(-30*24*time.Hour), 100, false)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	left := outboxIDs(ctx, t, repo)
	assert.NotContains(t, left, stale, "a row published past the retention window is pure history")
	assert.Contains(t, left, fresh, "a recently published row is still the debugging trail for a live deploy")
	assert.Contains(t, left, undelivered,
		"an unpublished row is undelivered work — no age makes it safe to delete, and the relay is the "+
			"only thing allowed to retire one")
}

func TestPurgeOutbox_KeepsAnAncientUnpublishedRow(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	stuck := seedOutbox(ctx, t, repo, "site.changed", nil)
	_, err := repo.pool.Exec(ctx, `UPDATE outbox SET created_at = $1 WHERE id = $2`,
		now.Add(-365*24*time.Hour), stuck)
	require.NoError(t, err)

	n, err := repo.PurgeOutbox(ctx, now, 100, false)
	require.NoError(t, err)

	assert.Equal(t, 0, n)
	assert.Contains(t, outboxIDs(ctx, t, repo), stuck,
		"the cutoff is published_at, never created_at; a year-old row the relay never delivered is a "+
			"backlog to investigate, not garbage to collect")
}

func TestPurgeOutbox_StopsAtTheBatchLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)

	for range 5 {
		seedOutbox(ctx, t, repo, "site.changed", &old)
	}

	n, err := repo.PurgeOutbox(ctx, now.Add(-30*24*time.Hour), 2, false)
	require.NoError(t, err)

	assert.Equal(t, 2, n, "one run must delete at most the batch limit so a large backlog cannot hold a "+
		"long transaction open; the remainder drains on the next nightly run")
	assert.Len(t, outboxIDs(ctx, t, repo), 3)
}

func TestPurgeOutbox_RefusesANonPositiveLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	kept := seedOutbox(ctx, t, repo, "site.changed", &old)

	n, err := repo.PurgeOutbox(ctx, time.Now().UTC(), 0, false)

	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Contains(t, outboxIDs(ctx, t, repo), kept,
		"a zero limit means no work, matching the blast cap's refuse-rather-than-unleash reading")
}

func TestPurgeOutbox_CountsWithoutDeletingOnADryRun(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)

	for range 3 {
		seedOutbox(ctx, t, repo, "site.changed", &old)
	}
	seedOutbox(ctx, t, repo, "site.changed", nil)

	n, err := repo.PurgeOutbox(ctx, now.Add(-30*24*time.Hour), 100, true)
	require.NoError(t, err)

	assert.Equal(t, 3, n, "a dry run reports exactly the set the live run would delete")
	assert.Len(t, outboxIDs(ctx, t, repo), 4, "and removes none of it")
}

func TestPurgeOutbox_DryRunHonoursTheBatchLimit(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-40 * 24 * time.Hour)

	for range 5 {
		seedOutbox(ctx, t, repo, "site.changed", &old)
	}

	n, err := repo.PurgeOutbox(ctx, now.Add(-30*24*time.Hour), 2, true)
	require.NoError(t, err)

	assert.Equal(t, 2, n,
		"the plan must report what one run would take, not the whole backlog, or the operator reads a "+
			"number the live run will not match")
}
