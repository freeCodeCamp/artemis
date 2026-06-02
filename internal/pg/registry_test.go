package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

func newTestRegistry(t *testing.T) *RegistryStore {
	t.Helper()
	repo := newTestRepo(t)
	return NewRegistryStore(&DB{Pool: repo.pool})
}

func TestRegistryPG(t *testing.T) {
	ctx := context.Background()
	var changed []string
	store := newTestRegistry(t).WithOnChange(func(slug string) { changed = append(changed, slug) })

	site, err := store.Register(ctx, "www", []string{"team-eng", "team-platform"}, "alice")
	require.NoError(t, err)
	assert.Equal(t, "www", site.Slug)
	assert.ElementsMatch(t, []string{"team-eng", "team-platform"}, site.Teams)

	_, err = store.Register(ctx, "www", []string{"x"}, "bob")
	assert.ErrorIs(t, err, registry.ErrAlreadyExists, "duplicate slug rejected")

	updated, err := store.UpdateTeams(ctx, "www", []string{"team-platform"})
	require.NoError(t, err)
	assert.Equal(t, []string{"team-platform"}, updated.Teams)
	assert.Equal(t, "alice", updated.CreatedBy, "created_by preserved across update")
	assert.True(t, !updated.UpdatedAt.Before(site.UpdatedAt))

	_, err = store.UpdateTeams(ctx, "absent", []string{"x"})
	assert.ErrorIs(t, err, registry.ErrNotFound)

	_, err = store.Register(ctx, "learn", []string{"team-eng"}, "carol")
	require.NoError(t, err)
	sites, err := store.Sites(ctx)
	require.NoError(t, err)
	require.Len(t, sites, 2)
	assert.Equal(t, "learn", sites[0].Slug, "sorted by slug ascending")
	assert.Equal(t, "www", sites[1].Slug)

	require.NoError(t, store.Delete(ctx, "www"))
	assert.ErrorIs(t, store.Delete(ctx, "www"), registry.ErrNotFound, "double delete -> not found")

	assert.Equal(t, []string{"www", "www", "learn", "www"}, changed,
		"registry.changed fires on register/update/register/delete for Valkey cache invalidation")
}
