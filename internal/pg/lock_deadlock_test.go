package pg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestWithSiteLock_NoPoolSelfDeadlock(t *testing.T) {
	t.Helper()
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
	poolCfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	require.NoError(t, err)
	t.Cleanup(func() { go pool.Close() })
	require.NoError(t, Migrate(ctx, pool))
	repo := &Repo{pool: pool}

	now := time.Now().UTC()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				site := []string{"a.freecode.camp", "b.freecode.camp"}[n]
				err := repo.WithSiteLock(ctx, site, func() error {
					return repo.FinalizeAtomic(ctx, site, "20260101-000000-aaaaaaa", "production", now, 0)
				})
				assert.NoError(t, err)
			}(i)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("nested pool acquire under site lock deadlocked at MaxConns=2 (B19)")
	}
}
