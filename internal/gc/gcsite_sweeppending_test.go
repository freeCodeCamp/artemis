package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestSiteGC_SweepPendingCollectsAbandonedRowsAndLeavesRetentionAlone(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	res, err := pendingSiteGC(t, store, mover, pending).SweepPending(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{"www/abandoned"}, store.tombstoned,
		"the nightly sweep exists for the abandoned pending row; retention stays with the event-driven gc-site run")
	assert.Equal(t, []string{"abandoned"}, res.Tombstoned)
	require.Len(t, mover.moves, 1)
	assert.Equal(t, [2]string{"www/deploys/abandoned/", "_trash/www/abandoned/"}, mover.moves[0])
}

func TestSiteGC_SweepPendingUsesTheSameGraceCutoffAsRun(t *testing.T) {
	pending := &fakePending{rows: map[string][]Deploy{}}

	_, err := pendingSiteGC(t, &fakeStore{deploys: map[string][]Deploy{}}, &fakeMover{}, pending).
		SweepPending(context.Background(), "www", false)
	require.NoError(t, err)

	require.Len(t, pending.cuts, 1)
	assert.Equal(t, testNow.Add(-testPolicy().Grace), pending.cuts[0],
		"a shorter cutoff here than in Run would reap a session that is still uploading")
}

func TestSiteGC_SweepPendingWithoutAPendingSourceIsANoOp(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}}

	res, err := newSiteGC(store, &fakeMover{}).SweepPending(context.Background(), "www", false)

	require.NoError(t, err, "pending expiry is optional wiring; its absence must not error the nightly cron")
	assert.Empty(t, res.Tombstoned)
	assert.Empty(t, store.tombstoned)
}

func TestSiteGC_SweepPendingDryRunWritesNothing(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	res, err := pendingSiteGC(t, store, mover, pending).SweepPending(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Contains(t, res.Planned, "abandoned")
	assert.Empty(t, store.tombstoned, "a dry run must not write")
	assert.Empty(t, mover.moves, "a dry run must not move bytes")
}

func TestSiteGC_SweepPendingRefusesASiteInsideItsReservationGrace(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	g := pendingSiteGC(t, store, mover, pending)
	g.Held = func(context.Context, sitekey.Dirname) (bool, error) { return true, nil }

	res, err := g.SweepPending(context.Background(), "www", false)
	require.NoError(t, err)

	assert.True(t, res.Held)
	assert.Empty(t, store.tombstoned,
		"the nightly all-sites path is the one that reaches a reserved site with no site.changed event; "+
			"collecting now would trash the bytes undelete restores")
	assert.Empty(t, mover.moves)
}

func TestSiteGC_SweepPendingSkipsARowThatFinalizedBeforeTheLock(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "raced", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	g := pendingSiteGC(t, store, mover, pending)
	g.PendingIDs = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}

	res, err := g.SweepPending(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{"raced"}, res.SkippedNotPending)
	assert.Empty(t, store.tombstoned,
		"the plan is built before the site lock and pg.Tombstone deletes without a state filter, so a row "+
			"finalize flipped to active in that gap must be revalidated under the lock, not trashed")
	assert.Empty(t, mover.moves)
}

func TestSiteGC_SweepPendingAbortsWhenTheRevalidationReadFails(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	g := pendingSiteGC(t, store, &fakeMover{}, pending)
	g.PendingIDs = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
		return nil, assert.AnError
	}

	_, err := g.SweepPending(context.Background(), "www", false)
	require.Error(t, err)
	assert.Empty(t, store.tombstoned)
}

func TestSiteGC_SweepPendingRefusesWithoutARevalidationReader(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	g := pendingSiteGC(t, store, &fakeMover{}, pending)
	g.PendingIDs = nil

	_, err := g.SweepPending(context.Background(), "www", false)
	require.Error(t, err, "sweeping without the under-lock revalidation reader is the wiring bug that reopens the race")
	assert.Empty(t, store.tombstoned)
}

func TestSiteGC_RunRevalidatesOnlyThePendingHalfOfThePlan(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "raced", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	g := pendingSiteGC(t, store, mover, pending)
	g.PendingIDs = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}

	res, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{"raced"}, res.SkippedNotPending,
		"PlanSite folds expired-pending rows into the same plan.Delete as retention, so the event-driven path "+
			"carries the identical plan-before-lock race the nightly sweep does")
	assert.NotEmpty(t, res.Tombstoned,
		"the revalidation must apply only to the pending half; vetoing retention deletes would stall collection")
	assert.NotContains(t, res.Tombstoned, "raced")
}
