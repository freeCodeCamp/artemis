package pg

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func fetchUnpublished(ctx context.Context, t *testing.T, repo *Repo, limit int) []OutboxEvent {
	t.Helper()
	rows, err := repo.pool.Query(ctx,
		`SELECT id, topic, payload FROM outbox
		 WHERE published_at IS NULL
		 ORDER BY id
		 LIMIT $1`, limit)
	require.NoError(t, err)
	defer rows.Close()

	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		require.NoError(t, rows.Scan(&e.ID, &e.Topic, &e.Payload))
		out = append(out, e)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestOutbox_AtomicWithMetadataAndRelay(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO deploys (site, id, mtime) VALUES ('www', 'd1', now())`); err != nil {
			return err
		}
		return Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": "www"})
	}))

	boom := errors.New("boom")
	err := repo.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO deploys (site, id, mtime) VALUES ('www', 'd2', now())`); err != nil {
			return err
		}
		if err := Enqueue(ctx, tx, TopicSiteChanged, map[string]string{"site": "rolled-back"}); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)

	deploys, err := repo.DeploysForSite(ctx, "www")
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, d := range deploys {
		ids[d.ID] = true
	}
	assert.True(t, ids["d1"], "committed metadata present")
	assert.False(t, ids["d2"], "rolled-back metadata absent (dual-write closed)")

	events := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, events, 1, "only the committed tx produced an outbox row")
	assert.Equal(t, TopicSiteChanged, events[0].Topic)
	var p map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &p))
	assert.Equal(t, "www", p["site"])

	require.NoError(t, repo.MarkPublished(ctx, []int64{events[0].ID}, time.Now()))
	again := fetchUnpublished(ctx, t, repo, 10)
	assert.Empty(t, again, "published events are not re-fetched")
}

func TestRelayBatch_CommitsClaimBeforePublish(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "www"))

	events := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, events, 1)
	id := events[0].ID

	started := make(chan struct{})
	release := make(chan struct{})
	publish := func(OutboxEvent) error {
		close(started)
		<-release
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := repo.RelayBatch(ctx, 10, publish, time.Now())
		done <- err
	}()

	<-started
	_, lockErr := repo.pool.Exec(ctx, `SELECT id FROM outbox WHERE id = $1 FOR UPDATE NOWAIT`, id)
	close(release)
	require.NoError(t, <-done)

	assert.NoError(t, lockErr, "claim tx must commit before publish runs; it must not hold the outbox row lock across external publish I/O")
}

func TestOutbox_EnqueueSiteChanged(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "learn"))

	events := fetchUnpublished(ctx, t, repo, 10)
	require.Len(t, events, 1)
	var p map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &p))
	assert.Equal(t, "learn", p["site"])
}

func TestTopicSiteLifecycleIsTheWireLiteral(t *testing.T) {
	assert.Equal(t, "site.lifecycle", TopicSiteLifecycle,
		"outbox rows already enqueued carry this literal; a rename leaves them with no consumer")
}

func TestRelayBatch_AFailedPublishDoesNotFreezeTheRest(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	for _, site := range []sitekey.Dirname{"a", "b", "c"} {
		require.NoError(t, repo.EnqueueSiteChanged(ctx, site))
	}

	calls := 0
	failSecond := func(OutboxEvent) error {
		calls++
		if calls == 2 {
			return errors.New("grpc hiccup")
		}
		return nil
	}
	n, err := repo.RelayBatch(ctx, 10, failSecond, time.Now())
	require.Error(t, err)
	assert.Equal(t, 1, n, "the first event is published before the failure")

	var maxWait time.Duration
	require.NoError(t, repo.pool.QueryRow(ctx,
		`SELECT coalesce(max(claim_expires_at - now()), interval '0') FROM outbox WHERE published_at IS NULL`).Scan(&maxWait))
	assert.LessOrEqual(t, maxWait, relayRetryBackoff, "the failed event and the one behind it back off for a minute, not the 5-minute claim")
	assert.Positive(t, maxWait, "they are not freed at once: a dead publisher must not be retried every relay tick")

	var published int
	retry := func(OutboxEvent) error { published++; return nil }
	n, err = repo.RelayBatch(ctx, 10, retry, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, n, "inside the backoff nothing is re-claimed")

	_, err = repo.pool.Exec(ctx, `UPDATE outbox SET claim_expires_at = now() - interval '1 second' WHERE published_at IS NULL`)
	require.NoError(t, err)
	n, err = repo.RelayBatch(ctx, 10, retry, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 2, n, "after the backoff both rows publish in id order")
	assert.Equal(t, 2, published)
	assert.Empty(t, fetchUnpublished(ctx, t, repo, 10))
}
