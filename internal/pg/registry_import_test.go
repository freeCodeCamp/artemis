package pg

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

func seededValkey(t *testing.T) *valkey.Store {
	t.Helper()
	mr := miniredis.RunT(t)
	ctx := context.Background()
	store, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Register(ctx, "www", []string{"team-eng", "team-platform"}, "alice")
	require.NoError(t, err)
	_, err = store.Register(ctx, "learn", []string{"team-eng"}, "carol")
	require.NoError(t, err)
	return store
}

func TestRegistryImportOnBoot(t *testing.T) {
	ctx := context.Background()
	pgStore := newTestRegistry(t)
	src := seededValkey(t)

	n, err := pgStore.Import(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "imports every seeded site")

	sites, err := pgStore.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, sites, 2)
	assert.Equal(t, "learn", sites[0].Slug)
	assert.Equal(t, []string{"team-eng"}, sites[0].Teams)
	assert.Equal(t, "www", sites[1].Slug)
	assert.ElementsMatch(t, []string{"team-eng", "team-platform"}, sites[1].Teams)

	n2, err := pgStore.Import(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second boot is a no-op")

	after, err := pgStore.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, after, 2, "no duplicate rows on re-run")
}

func TestRegistryImportOnBoot_NoClobberWhenPGNonEmpty(t *testing.T) {
	ctx := context.Background()
	pgStore := newTestRegistry(t)

	_, err := pgStore.Register(ctx, "www", []string{"newer-team"}, "operator")
	require.NoError(t, err)

	src := seededValkey(t)
	n, err := pgStore.Import(ctx, src)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "PG-non-empty boot does not import")

	sites, err := pgStore.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, sites, 1, "Valkey rows do not clobber existing PG data")
	assert.Equal(t, "www", sites[0].Slug)
	assert.Equal(t, []string{"newer-team"}, sites[0].Teams)
}

func TestRegistryImportOnBoot_PreservesTimestamps(t *testing.T) {
	ctx := context.Background()
	pgStore := newTestRegistry(t)

	mr := miniredis.RunT(t)
	src, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	created := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	src.Now = func() time.Time { return created }
	_, err = src.Register(ctx, "www", []string{"team-eng"}, "alice")
	require.NoError(t, err)

	n, err := pgStore.Import(ctx, src)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	sites, err := pgStore.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, sites, 1)
	assert.WithinDuration(t, created, sites[0].CreatedAt, time.Second)
	assert.Equal(t, "alice", sites[0].CreatedBy)
}
