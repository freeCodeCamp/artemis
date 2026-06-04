package pg

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestReleaseAdvisoryLock_FreesLockOnCanceledCallerCtx(t *testing.T) {
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

	poolCfg, err := pgxpool.ParseConfig(connStr)
	require.NoError(t, err)
	poolCfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	probe, err := pgxpool.New(ctx, connStr)
	require.NoError(t, err)
	t.Cleanup(probe.Close)

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)

	var poolPID uint32
	require.NoError(t, conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&poolPID))

	callerCtx, cancel := context.WithCancel(ctx)
	_, err = conn.Exec(callerCtx, "SELECT pg_advisory_lock($1)", migrateAdvisoryLockKey)
	require.NoError(t, err)

	held, err := advisoryLockHeldByPID(ctx, probe, migrateAdvisoryLockKey, poolPID)
	require.NoError(t, err)
	require.True(t, held, "lock acquired on the pooled session")

	cancel()
	require.Error(t, callerCtx.Err(), "caller ctx is canceled before the deferred unlock runs")

	releaseAdvisoryLock(conn, migrateAdvisoryLockKey)
	conn.Release()

	held, err = advisoryLockHeldByPID(ctx, probe, migrateAdvisoryLockKey, poolPID)
	require.NoError(t, err)
	require.False(t, held,
		"releaseAdvisoryLock must free the lock on the pooled session even when the caller ctx was canceled; a held lock leaks onto the pooled conn and blocks later migrations")
}

func advisoryLockHeldByPID(ctx context.Context, pool *pgxpool.Pool, key int64, pid uint32) (bool, error) {
	var held bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND objid = $1 AND granted AND pid = $2)`,
		key, pid).Scan(&held)
	return held, err
}
