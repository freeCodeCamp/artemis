package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetAliasCAS_CreateFromEmpty(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC()

	cur, ok, err := repo.SetAliasCAS(ctx, "new-site", "production", "", "d1", t0)
	require.NoError(t, err)
	assert.True(t, ok, "CAS over an absent row with expected=\"\" creates the alias")
	assert.Equal(t, "", cur, "current value of a fresh alias is empty")

	targets, _, err := repo.AliasTargets(ctx, "new-site")
	require.NoError(t, err)
	assert.Contains(t, targets, "d1", "the new alias points at the published deploy")

	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, events, 1, "first-publish enqueues exactly one outbox event")
	assert.Equal(t, TopicSiteChanged, events[0].Topic)
}

func TestSetAliasCAS_AbsentRowNonEmptyExpected(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	t0 := time.Now().UTC()

	cur, ok, err := repo.SetAliasCAS(ctx, "ghost-site", "production", "X", "d1", t0)
	require.NoError(t, err)
	assert.False(t, ok, "CAS over an absent row with a non-empty expected value is rejected")
	assert.Equal(t, "", cur, "actual current value is empty for an absent row")

	targets, _, err := repo.AliasTargets(ctx, "ghost-site")
	require.NoError(t, err)
	assert.Empty(t, targets, "rejected CAS does not create the alias")

	events, err := repo.FetchUnpublished(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, events, "rejected CAS enqueues no outbox event")
}
