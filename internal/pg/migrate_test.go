package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMigrations(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("artemis_test"),
		postgres.WithUsername("artemis"),
		postgres.WithPassword("artemis"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := New(ctx, Config{DatabaseURL: connStr})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	require.NoError(t, Migrate(ctx, db.Pool))
	require.NoError(t, Migrate(ctx, db.Pool), "re-run must be idempotent")

	for _, table := range []string{"deploys", "aliases", "tombstones", "outbox", "schema_migrations"} {
		var exists bool
		err := db.Pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)",
			table).Scan(&exists)
		require.NoError(t, err)
		require.Truef(t, exists, "table %q must exist after migrate", table)
	}

	names, err := migrationFiles()
	require.NoError(t, err)
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	require.Equal(t, len(names), count, "each migration recorded exactly once")

	var applied bool
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)",
		"0004_outbox_id_index.sql").Scan(&applied))
	require.True(t, applied, "0004 recorded")

	var indexDef string
	require.NoError(t, db.Pool.QueryRow(ctx,
		"SELECT indexdef FROM pg_indexes WHERE indexname = 'outbox_unpublished_idx'").Scan(&indexDef))
	require.Contains(t, indexDef, "(id)", "0004 rebuilt outbox_unpublished_idx on id to match FetchUnpublished ORDER BY id")
	require.NotContains(t, indexDef, "created_at", "stale created_at index dropped by 0004")

	repo := NewRepo(db)
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "second"))
	require.NoError(t, repo.EnqueueSiteChanged(ctx, "third"))
	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 2, "both enqueued events unpublished")
	require.Less(t, events[0].ID, events[1].ID, "FetchUnpublished returns oldest-first by id")
}
