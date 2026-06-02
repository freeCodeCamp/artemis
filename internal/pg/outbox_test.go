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
)

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

	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "only the committed tx produced an outbox row")
	assert.Equal(t, TopicSiteChanged, events[0].Topic)
	var p map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &p))
	assert.Equal(t, "www", p["site"])

	require.NoError(t, repo.MarkPublished(ctx, []int64{events[0].ID}, time.Now()))
	again, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, again, "published events are not re-fetched")
}

func TestOutbox_EnqueueSiteChanged(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "learn"))

	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	var p map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &p))
	assert.Equal(t, "learn", p["site"])
}
