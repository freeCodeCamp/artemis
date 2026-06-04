package teamcache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTeamCache_GetOrFetch_AbortsOnGetError(t *testing.T) {
	ctx := context.Background()
	c, mr := newTestCache(t, time.Minute)
	mr.SetError("valkey down")

	calls := 0
	_, err := c.GetOrFetch(ctx, "b", func(context.Context) ([]string, error) {
		calls++
		return nil, nil
	})
	require.Error(t, err)
	assert.Equal(t, 0, calls, "fetch must not run when the cache Get itself errors")
	assert.ErrorContains(t, err, "teamcache get b")
}
