package main

import (
	"context"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestBootMigrations(t *testing.T) {
	ctx := context.Background()

	db, cleanup, err := openPostgres(ctx, &config.Config{})
	require.NoError(t, err, "empty DATABASE_URL must not error")
	require.Nil(t, db, "no DATABASE_URL -> no pool (deploy-only mode)")
	require.NotNil(t, cleanup, "cleanup must be safe to call when gated off")
	cleanup()

	testcontainers.SkipIfProviderIsNotHealthy(t)

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

	db, cleanup, err = openPostgres(ctx, &config.Config{DatabaseURL: connStr})
	require.NoError(t, err)
	require.NotNil(t, db, "DATABASE_URL set -> pool opened")
	t.Cleanup(cleanup)

	for _, table := range []string{"deploys", "aliases", "tombstones", "outbox", "schema_migrations"} {
		var exists bool
		require.NoError(t, db.Pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = $1)",
			table).Scan(&exists))
		require.Truef(t, exists, "table %q must exist after boot migrations", table)
	}
}
