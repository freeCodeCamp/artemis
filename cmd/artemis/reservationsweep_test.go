package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
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

func (r *recordingReleaser) ReleaseReservation(_ context.Context, slug sitekey.Slug) error {
	if r.err != nil {
		return r.err
	}
	r.released = append(r.released, slug)
	return nil
}

type recordingLocker struct {
	mu       sync.Mutex
	sessions int
	locked   []sitekey.Dirname
	held     []sitekey.Dirname
	newErr   error
	lockErr  error
	closed   int
}

func (l *recordingLocker) NewLockSession(context.Context) (gc.LockSession, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.newErr != nil {
		return nil, l.newErr
	}
	l.sessions++
	return &recordingLockSession{parent: l}, nil
}

type recordingLockSession struct{ parent *recordingLocker }

func (s *recordingLockSession) WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error {
	l := s.parent
	l.mu.Lock()
	if l.lockErr != nil {
		err := l.lockErr
		l.mu.Unlock()
		return err
	}
	l.locked = append(l.locked, site)
	l.held = append(l.held, site)
	l.mu.Unlock()
	err := fn(ctx)
	l.mu.Lock()
	l.held = l.held[:len(l.held)-1]
	l.mu.Unlock()
	return err
}

func (s *recordingLockSession) Close(context.Context) {
	s.parent.mu.Lock()
	s.parent.closed++
	s.parent.mu.Unlock()
}

func testDirname(s sitekey.Slug) sitekey.Dirname {
	return sitekey.Dirname(string(s) + ".freecode.camp")
}

func lockOnlyDeps(l *recordingLocker) reclaimDeps {
	return reclaimDeps{Locker: l, Dirname: testDirname, TrashBase: "_trash/"}
}

func fixedNow() time.Time { return time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC) }

func TestSweepExpiredReservations_FreesOnlyNamesPastTheirGrace(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{
		{Slug: "old-one", ReservedUntil: fixedNow().Add(-time.Hour)},
		{Slug: "old-two", ReservedUntil: fixedNow().Add(-time.Minute)},
	}}
	rel := &recordingReleaser{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, lockOnlyDeps(&recordingLocker{}), fixedNow, false)

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
	lk := &recordingLocker{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, lockOnlyDeps(lk), fixedNow, true)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, rel.released, "CLEANUP_DRY_RUN must reach every destructive leg, including this one")
	assert.Zero(t, lk.sessions, "a dry run touches nothing, so it must not take a lock either")
}

func TestSweepExpiredReservations_ContinuesPastAFailingSiteAndSurfacesTheFailure(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{
		{Slug: "wedged", ReservedUntil: fixedNow().Add(-time.Hour)},
		{Slug: "healthy", ReservedUntil: fixedNow().Add(-time.Hour)},
	}}
	rel := &recordingReleaser{}
	deps := lockOnlyDeps(&recordingLocker{})
	deps.Mover = &failingSiteMover{fail: "wedged.freecode.camp"}
	deps.Tombstone = &recordingTombstones{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, deps, fixedNow, false)

	require.Error(t, err, "a site the sweep could not reclaim must surface, never be swallowed")
	assert.Equal(t, 1, n)
	assert.Equal(t, []sitekey.Slug{"healthy"}, rel.released,
		"ExpiredReservations sorts by deadline ascending, so a permanently failing first row would block every later row forever")
}

func TestSweepExpiredReservations_ReclaimsAndReleasesUnderTheSiteLock(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "gone", ReservedUntil: fixedNow().Add(-time.Hour)}}}
	lk := &recordingLocker{}
	mover := &lockAssertingMover{locker: lk}
	rel := &lockAssertingReleaser{locker: lk}
	deps := lockOnlyDeps(lk)
	deps.Mover = mover
	deps.Tombstone = &recordingTombstones{}

	n, err := sweepExpiredReservations(context.Background(), src, rel, deps, fixedNow, false)

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []sitekey.Dirname{"gone.freecode.camp"}, lk.locked,
		"an unlocked MovePrefix races SiteRelease on the same prefix and deletes an object the peer is still copying")
	assert.True(t, mover.sawLock, "the origin-prefix move must run inside the site lock")
	assert.True(t, rel.sawLock, "freeing the name must run inside the same lock that moved its bytes")
	assert.Equal(t, 1, lk.closed, "the lock session must be released when the run ends")
}

func TestSweepExpiredReservations_LiveRunWithoutALockerIsAWiringError(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{{Slug: "gone", ReservedUntil: fixedNow().Add(-time.Hour)}}}
	rel := &recordingReleaser{}

	n, err := sweepExpiredReservations(context.Background(), src, rel,
		reclaimDeps{Dirname: testDirname, TrashBase: "_trash/"}, fixedNow, false)

	require.Error(t, err, "a sweep wired without a Locker must refuse, not silently reclaim unlocked")
	assert.Zero(t, n)
	assert.Empty(t, rel.released)
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

type failingSiteMover struct {
	fail  sitekey.Dirname
	moved [][2]string
}

func (m *failingSiteMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	if src == string(m.fail)+"/" {
		return 0, errors.New("r2 down for this site")
	}
	m.moved = append(m.moved, [2]string{src, dst})
	return 3, nil
}

type lockAssertingMover struct {
	locker  *recordingLocker
	sawLock bool
}

func (m *lockAssertingMover) MovePrefix(_ context.Context, _, _ string) (int, error) {
	m.locker.mu.Lock()
	m.sawLock = len(m.locker.held) > 0
	m.locker.mu.Unlock()
	return 3, nil
}

type lockAssertingReleaser struct {
	locker  *recordingLocker
	sawLock bool
}

func (r *lockAssertingReleaser) ReleaseReservation(context.Context, sitekey.Slug) error {
	r.locker.mu.Lock()
	r.sawLock = len(r.locker.held) > 0
	r.locker.mu.Unlock()
	return nil
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
		Locker:    &recordingLocker{},
		Dirname:   testDirname,
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

func TestSweepExpiredReservations_SkipsARowWhoseClaimWasLostAndKeepsGoing(t *testing.T) {
	src := &scriptedReservations{expired: []registry.Reservation{
		{Slug: "gone", ReservedUntil: fixedNow().Add(-time.Hour)},
		{Slug: "still-here", ReservedUntil: fixedNow().Add(-time.Hour)},
	}}
	rel := &refusingReleaser{refuse: "gone"}

	n, err := sweepExpiredReservations(context.Background(), src, rel, lockOnlyDeps(&recordingLocker{}), fixedNow, false)

	require.NoError(t, err, "one row that stopped being an expired reservation is not a sweep failure")
	assert.Equal(t, 1, n)
	assert.Equal(t, []sitekey.Slug{"still-here"}, rel.released,
		"a name the guard refused must not be counted as released")
}

type refusingReleaser struct {
	refuse   sitekey.Slug
	released []sitekey.Slug
}

func (r *refusingReleaser) ReleaseReservation(_ context.Context, slug sitekey.Slug) error {
	if slug == r.refuse {
		return registry.ErrNotFound
	}
	r.released = append(r.released, slug)
	return nil
}
