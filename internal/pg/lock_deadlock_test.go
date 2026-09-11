package pg

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func newMaxConns2Repo(t *testing.T) *Repo {
	t.Helper()
	ctx := context.Background()
	poolCfg, err := pgxpool.ParseConfig(migratedTestDSN(t))
	require.NoError(t, err)
	poolCfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	require.NoError(t, err)
	t.Cleanup(func() { go pool.Close() })
	return &Repo{pool: pool}
}

func TestWithSiteLock_NoPoolSelfDeadlock(t *testing.T) {
	repo := newMaxConns2Repo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				site := []sitekey.Dirname{"a.freecode.camp", "b.freecode.camp"}[n]
				err := repo.WithSiteLock(ctx, site, func(context.Context) error {
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

func TestLockSession_NoPoolSelfDeadlock(t *testing.T) {
	repo := newMaxConns2Repo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				site := []sitekey.Dirname{"a.freecode.camp", "b.freecode.camp"}[n]
				sess, err := repo.NewLockSession(ctx)
				assert.NoError(t, err)
				defer sess.Close(ctx)
				err = sess.WithSiteLock(ctx, site, func(context.Context) error {
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
		t.Fatal("nested pool acquire under lock session deadlocked at MaxConns=2 (B19 session path)")
	}
}
