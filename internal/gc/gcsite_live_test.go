package gc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGC_RollbackMidRun_R2Authority(t *testing.T) {
	store := &fakeStore{
		deploys:    map[string][]Deploy{"www": sixOld()},
		targetsSeq: []map[string]struct{}{{}},
	}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)

	calls := 0
	g.LiveAliases = func(_ context.Context, _ string) (map[string]struct{}, error) {
		calls++
		if calls >= 2 {
			return aliasSet("f-old"), nil
		}
		return map[string]struct{}{}, nil
	}

	res, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Contains(t, res.SkippedAliased, "f-old",
		"deploy made live in R2 mid-run must be skipped even though PG never saw the alias (B17)")
	for _, m := range mover.moves {
		assert.NotContains(t, m[0], "f-old", "live-aliased deploy bytes must not move to trash")
	}
	assert.GreaterOrEqual(t, calls, 2, "live R2 aliases re-read per move, not once per run")
}

func TestGC_LiveRunRequiresLiveAliasReader(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	g := newSiteGC(store, &fakeMover{})
	g.LiveAliases = nil

	_, err := g.Run(context.Background(), "www", false)
	assert.Error(t, err, "live run without an R2 alias reader is a wiring bug, fail loud")

	_, err = g.Run(context.Background(), "www", true)
	assert.NoError(t, err, "dry-run plans without touching R2")
}
