package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func (l *recordingLocker) holding() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.held) > 0
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

type scriptedClaimer struct {
	won      bool
	winsOnce bool
	err      error
	calls    []sitekey.Slug
	ttl      time.Duration
}

func (c *scriptedClaimer) ClaimReclaim(_ context.Context, slug sitekey.Slug, ttl time.Duration) (bool, error) {
	c.calls = append(c.calls, slug)
	c.ttl = ttl
	if c.winsOnce {
		return len(c.calls) == 1, c.err
	}
	return c.won, c.err
}

type recordingMover struct {
	moved   [][2]string
	err     error
	onMove  func()
	locker  *recordingLocker
	sawLock bool
}

func (m *recordingMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	if m.onMove != nil {
		m.onMove()
	}
	if m.locker != nil {
		m.sawLock = m.locker.holding()
	}
	if m.err != nil {
		return 0, m.err
	}
	m.moved = append(m.moved, [2]string{src, dst})
	return 3, nil
}

type recordingTombstones struct{ sites []sitekey.Dirname }

func (r *recordingTombstones) RecordSiteTombstone(_ context.Context, site sitekey.Dirname) error {
	r.sites = append(r.sites, site)
	return nil
}

type recordingAuditedReleaser struct {
	released []sitekey.Slug
	events   []pg.AuditEvent
	err      error
	locker   *recordingLocker
	sawLock  bool
	mover    *recordingMover
	sawMove  bool
}

func (r *recordingAuditedReleaser) ReleaseReservationAudited(_ context.Context, slug sitekey.Slug, e pg.AuditEvent) error {
	if r.locker != nil {
		r.sawLock = r.locker.holding()
	}
	if r.mover != nil {
		r.sawMove = len(r.mover.moved) > 0
	}
	if r.err != nil {
		return r.err
	}
	r.released = append(r.released, slug)
	r.events = append(r.events, e)
	return nil
}

func claimExpired(context.Context, sitekey.Slug) (bool, error) { return true, nil }

func reclaimInput(slug string) map[string]any {
	return map[string]any{"action": pg.LifecycleActionReclaim, "slug": slug, "site": slug + ".freecode.camp"}
}

type reclaimFixture struct {
	locker   *recordingLocker
	claimer  *scriptedClaimer
	mover    *recordingMover
	tb       *recordingTombstones
	releaser *recordingAuditedReleaser
}

func newReclaimFixture() (reclaimFixture, reclaimDeps) {
	lk := &recordingLocker{}
	mover := &recordingMover{locker: lk}
	f := reclaimFixture{
		locker:   lk,
		claimer:  &scriptedClaimer{won: true},
		mover:    mover,
		tb:       &recordingTombstones{},
		releaser: &recordingAuditedReleaser{locker: lk, mover: mover},
	}
	return f, reclaimDeps{
		Mover:     f.mover,
		Tombstone: f.tb,
		Locker:    f.locker,
		Expired:   claimExpired,
		Claimer:   f.claimer,
		Releaser:  f.releaser,
		Dirname:   testDirname,
		TrashBase: "_trash/",
	}
}

func TestRunSiteReclaim_ClaimsThenMovesThenReleasesUnderTheSiteLock(t *testing.T) {
	f, deps := newReclaimFixture()

	err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false)

	require.NoError(t, err)
	assert.Equal(t, []sitekey.Slug{"gone"}, f.claimer.calls)
	assert.Equal(t, reclaimClaimTTL, f.claimer.ttl,
		"the TTL decides how long a duplicate event stays a no-op; a shorter one lets two runs move the same site")
	assert.Equal(t, []sitekey.Dirname{"gone.freecode.camp"}, f.locker.locked,
		"an unlocked MovePrefix races SiteRelease on the same prefix and deletes an object the peer is still copying")
	assert.Equal(t, 1, f.locker.sessions, "one lock session per run, opened once")
	assert.Equal(t, 1, f.locker.closed, "the lock session must be released when the run ends")
	require.Len(t, f.mover.moved, 1,
		"tombstone-purge collects _trash only; bytes left at the origin have no collector and no alert")
	assert.Equal(t, [2]string{"gone.freecode.camp/", "_trash/gone.freecode.camp/"}, f.mover.moved[0],
		"the dirname keeps its name under _trash/, which is where the purge and any recovery look for it")
	assert.True(t, f.mover.sawLock, "the origin-prefix move must run inside the site lock")
	assert.True(t, f.releaser.sawLock, "freeing the name must run inside the same lock that moved its bytes")
	assert.True(t, f.releaser.sawMove, "the name is freed only after its bytes moved")
	assert.Equal(t, []sitekey.Slug{"gone"}, f.releaser.released)
}

func TestRunSiteReclaim_WritesTheTombstoneBeforeTheMove(t *testing.T) {
	f, deps := newReclaimFixture()
	var tombstonedAtMove int
	f.mover.onMove = func() { tombstonedAtMove = len(f.tb.sites) }

	require.NoError(t, runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false))

	assert.Equal(t, 1, tombstonedAtMove,
		"without a tombstone row nothing ever collects the trashed bytes; it must exist before the first object moves")
	assert.Equal(t, []sitekey.Dirname{"gone.freecode.camp"}, f.tb.sites)
}

func TestRunSiteReclaim_AuditsTheReleaseExactlyOnceWithTheMoveCount(t *testing.T) {
	f, deps := newReclaimFixture()

	require.NoError(t, runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false))

	require.Len(t, f.releaser.events, 1)
	e := f.releaser.events[0]
	assert.Equal(t, "system:gc", e.Actor, "audit readers separate system rows from staff rows by actor")
	assert.Equal(t, "site.reclaim", e.Action, "COMPATIBILITY entry 33 names site.reclaim; any other action hides the row from every filter and dashboard keyed on it")
	assert.Equal(t, "gone.freecode.camp", e.Site)
	assert.Equal(t, "success", e.Outcome)
	assert.Equal(t, map[string]any{"moved": 3, "tombstoned": true}, e.Detail)
}

func TestRunSiteReclaim_LostClaimTouchesNothing(t *testing.T) {
	f, deps := newReclaimFixture()
	f.claimer.won = false

	err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false)

	require.NoError(t, err, "a duplicate or late event is not a failure; the effect already happened or belongs to another run")
	assert.Zero(t, f.locker.sessions, "no claim, no lock")
	assert.Empty(t, f.mover.moved)
	assert.Empty(t, f.tb.sites)
	assert.Empty(t, f.releaser.released)
}

func TestRunSiteReclaim_KeepsTheNameWhenTheMoveFails(t *testing.T) {
	f, deps := newReclaimFixture()
	f.mover.err = errors.New("r2 down")

	err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false)

	require.Error(t, err)
	assert.Empty(t, f.releaser.released,
		"freeing the name while its bytes are still at the origin lets a new owner inherit them")
	assert.Empty(t, f.releaser.events, "no audit row without the deleting transaction")
}

func TestRunSiteReclaim_RefusesARowTheDatabaseNoLongerHoldsExpired(t *testing.T) {
	f, deps := newReclaimFixture()
	deps.Expired = func(context.Context, sitekey.Slug) (bool, error) { return false, nil }

	err := runSiteReclaim(context.Background(), deps, reclaimInput("reborn"), false)

	require.NoError(t, err)
	assert.Empty(t, f.mover.moved,
		"between the claim and the lock the row stopped being an expired reservation; a new owner's whole object tree must not move to _trash")
	assert.Empty(t, f.tb.sites,
		"a site tombstone for a live site makes tombstone-purge purge a new owner's bytes seven days later")
	assert.Empty(t, f.releaser.released)
}

func TestRunSiteReclaim_AClaimLostAfterTheMoveIsNotAFailure(t *testing.T) {
	f, deps := newReclaimFixture()
	f.releaser.err = registry.ErrNotFound

	err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false)

	require.NoError(t, err, "the row stopped being an expired reservation after its bytes were trashed; the bytes sit in _trash under a tombstone and the purge collects them")
	assert.Len(t, f.mover.moved, 1, "the bytes moved before the release was refused; they sit in _trash under a tombstone")
}

func TestRunSiteReclaim_DryRunClaimsNothingAndMovesNothing(t *testing.T) {
	f, deps := newReclaimFixture()

	err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), true)

	require.NoError(t, err)
	assert.Empty(t, f.claimer.calls, "a dry run must not set reclaim_started_at; that would hide the row from the next live run for a whole TTL")
	assert.Zero(t, f.locker.sessions)
	assert.Empty(t, f.mover.moved)
	assert.Empty(t, f.releaser.released)
}

func TestRunSiteReclaim_RejectsAnInputWhoseSiteIsNotTheSlugsDirname(t *testing.T) {
	f, deps := newReclaimFixture()
	input := reclaimInput("gone")
	input["site"] = "someone-else.freecode.camp"

	err := runSiteReclaim(context.Background(), deps, input, false)

	require.Error(t, err, "the prefix moved is derived from the slug; a payload that names another prefix is a wiring or template drift and must fail loudly")
	assert.Empty(t, f.claimer.calls)
	assert.Empty(t, f.mover.moved)
}

func TestRunSiteReclaim_RejectsMissingOrForeignInput(t *testing.T) {
	cases := map[string]map[string]any{
		"empty":          {},
		"no-slug":        {"action": pg.LifecycleActionReclaim, "site": "x.freecode.camp"},
		"other-action":   {"action": "unpublish", "slug": "gone", "site": "gone.freecode.camp"},
		"missing-action": {"slug": "gone", "site": "gone.freecode.camp"},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			f, deps := newReclaimFixture()
			err := runSiteReclaim(context.Background(), deps, input, false)
			require.Error(t, err)
			assert.Empty(t, f.claimer.calls, "nothing is claimed for an input the step does not understand")
		})
	}
}

func TestRunSiteReclaim_LiveRunWithoutItsDependenciesIsAWiringError(t *testing.T) {
	cases := map[string]func(*reclaimDeps){
		"no-locker":    func(d *reclaimDeps) { d.Locker = nil },
		"no-expired":   func(d *reclaimDeps) { d.Expired = nil },
		"no-claimer":   func(d *reclaimDeps) { d.Claimer = nil },
		"no-releaser":  func(d *reclaimDeps) { d.Releaser = nil },
		"no-tombstone": func(d *reclaimDeps) { d.Tombstone = nil },
		"no-dirname":   func(d *reclaimDeps) { d.Dirname = nil },
		"no-mover":     func(d *reclaimDeps) { d.Mover = nil },
	}
	for name, strip := range cases {
		t.Run(name, func(t *testing.T) {
			f, deps := newReclaimFixture()
			strip(&deps)
			err := runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false)
			require.Error(t, err, "a reclaim wired without its guards must refuse, not silently destroy unlocked or unaudited")
			assert.Empty(t, f.mover.moved)
			assert.Empty(t, f.releaser.released)
		})
	}
}

func TestRunSiteReclaim_TheSameEventTwiceReclaimsOnce(t *testing.T) {
	f, deps := newReclaimFixture()
	f.claimer.winsOnce = true

	require.NoError(t, runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false))
	require.NoError(t, runSiteReclaim(context.Background(), deps, reclaimInput("gone"), false))

	assert.Len(t, f.claimer.calls, 2, "both deliveries reach the claim; the claim is what decides")
	assert.Len(t, f.mover.moved, 1, "an at-least-once outbox can deliver one event twice; the dirname must move once")
	assert.Len(t, f.releaser.events, 1, "exactly one audit row per reclaimed site, however many times the event arrives")
	assert.Equal(t, 1, f.locker.sessions, "the losing run never takes the lock, so it never holds a connection")
}

func TestReclaimClaimTTL_ReemitsNextNight(t *testing.T) {
	assert.Less(t, reclaimClaimTTL+reclaimBatchWorstCase, 24*time.Hour,
		"a claim taken by the last run of a full batch must be older than the TTL at the next 03:00 sweep")
}

func TestSiteReclaimOpIsCronShaped(t *testing.T) {
	assert.True(t, observability.IsCronShaped(opSiteReclaim),
		"a nightly reclaim failure must reach Sentry every time, never through the transient-rate tracker")
}
