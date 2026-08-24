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

	n, err := sweepExpiredReservations(context.Background(), src, rel, fixedNow, false)

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

	n, err := sweepExpiredReservations(context.Background(), src, rel, fixedNow, true)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released, "CLEANUP_DRY_RUN must reach every destructive leg, including this one")
}

func TestSweepExpiredReservations_StopsAtTheFirstReleaseFailure(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "a"}, {Slug: "b"}}}
	rel := &recordingReleaser{err: errors.New("pg down")}

	n, err := sweepExpiredReservations(context.Background(), src, rel, fixedNow, false)

	require.Error(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released,
		"a half-swept run must surface, not report success over a failure it walked past")
}

func TestSweepExpiredReservations_NoStoreIsANoOp(t *testing.T) {
	n, err := sweepExpiredReservations(context.Background(), nil, nil, fixedNow, false)
	require.NoError(t, err)
	assert.Zero(t, n)
}
