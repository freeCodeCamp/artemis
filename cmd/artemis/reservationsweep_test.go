package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedReservations struct {
	expired []registry.Reservation
	before  time.Time
	err     error
}

func (s *scriptedReservations) ExpiredReservations(_ context.Context, before time.Time, limit int) ([]registry.Reservation, error) {
	s.before = before
	if s.err != nil {
		return nil, s.err
	}
	if len(s.expired) > limit {
		return s.expired[:limit], nil
	}
	return s.expired, nil
}

type recordingReleaser struct {
	released []sitekey.Slug
	err      error
}

func (r *recordingReleaser) Delete(_ context.Context, slug sitekey.Slug) error {
	if r.err != nil {
		return r.err
	}
	r.released = append(r.released, slug)
	return nil
}

func fixedNow() time.Time { return time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC) }

func TestSweepExpiredReservations_FreesOnlyNamesPastTheirGrace(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{
		{Slug: "old-one", ReservedUntil: fixedNow().Add(-time.Hour)},
		{Slug: "old-two", ReservedUntil: fixedNow().Add(-time.Minute)},
	}}
	rel := &recordingReleaser{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, reclaimDeps{}, fixedNow, false)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []sitekey.Slug{"old-one", "old-two"}, rel.released,
		"without this sweep a delete holds the name forever and the grace period is a floor, not a ceiling")
	assert.Equal(t, fixedNow(), src.before,
		"the cutoff must be now; a future cutoff would free names still inside their grace")
}

func TestSweepExpiredReservations_DryRunReleasesNothing(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "old-one"}}}
	rel := &recordingReleaser{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, reclaimDeps{}, fixedNow, true)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released, "CLEANUP_DRY_RUN must reach every destructive leg, including this one")
}

func TestSweepExpiredReservations_StopsAtTheFirstReleaseFailure(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "a"}, {Slug: "b"}}}
	rel := &recordingReleaser{err: errors.New("pg down")}

	n, err := sweepExpiredReservations(context.Background(), src, rel, reclaimDeps{}, fixedNow, false)

	require.Error(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released,
		"a half-swept run must surface, not report success over a failure it walked past")
}

func TestSweepExpiredReservations_NoStoreIsANoOp(t *testing.T) {
	n, err := sweepExpiredReservations(context.Background(), nil, nil, reclaimDeps{}, fixedNow, false)
	require.NoError(t, err)
	assert.Zero(t, n)
}

type recordingMover struct {
	moved [][2]string
	err   error
}

func (m *recordingMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.moved = append(m.moved, [2]string{src, dst})
	return 3, nil
}

type recordingTombstones struct{ sites []sitekey.Dirname }

func (r *recordingTombstones) RecordSitePurge(_ context.Context, site sitekey.Dirname) error {
	r.sites = append(r.sites, site)
	return nil
}

func testReclaimDeps(m *recordingMover, tb *recordingTombstones) reclaimDeps {
	return reclaimDeps{
		Mover:     m,
		Tombstone: tb,
		Dirname:   func(s sitekey.Slug) sitekey.Dirname { return sitekey.Dirname(string(s) + ".freecode.camp") },
		TrashBase: "_trash/",
	}
}

func TestSweepExpiredReservations_MovesOriginBytesBeforeFreeingTheName(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "gone"}}}
	rel := &recordingReleaser{}
	mover := &recordingMover{}
	tb := &recordingTombstones{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, testReclaimDeps(mover, tb), fixedNow, false)

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.Len(t, mover.moved, 1,
		"tombstone-purge collects _trash only; bytes left at the origin have no collector and no alert")
	assert.Equal(t, [2]string{"gone.freecode.camp/", "_trash/gone.freecode.camp/"}, mover.moved[0])
	require.Len(t, tb.sites, 1,
		"without a tombstone row nothing ever collects the trashed bytes either")
	assert.Equal(t, sitekey.Dirname("gone.freecode.camp"), tb.sites[0])
	assert.Equal(t, []sitekey.Slug{"gone"}, rel.released)
}

func TestSweepExpiredReservations_KeepsTheNameWhenTheReclaimFails(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "gone"}}}
	rel := &recordingReleaser{}
	mover := &recordingMover{err: errors.New("r2 down")}

	n, err := sweepExpiredReservations(context.Background(), src, rel, testReclaimDeps(mover, &recordingTombstones{}), fixedNow, false)

	require.Error(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released,
		"freeing the name while its bytes are still at the origin lets a new owner inherit them")
}
