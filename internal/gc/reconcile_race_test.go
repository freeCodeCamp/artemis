package gc

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type racingLister struct {
	site     []string
	perID    map[string][]string
	perIDErr map[string]error
	listing  []string
}

func (l *racingLister) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	l.listing = append(l.listing, prefix)
	if err, ok := l.perIDErr[prefix]; ok {
		return nil, err
	}
	if keys, ok := l.perID[prefix]; ok {
		return keys, nil
	}
	return keysUnder(l.site, prefix), nil
}

type racingStore struct {
	deploys          map[string]Deploy
	aliases          map[string]struct{}
	tombstones       map[string]bool
	deploysCalls     int
	deploysErrOnCall int
	aliasCalls       int
	aliasErrOnCall   int
	reindexed        []string
	tombstoned       []string
	pruned           []string
}

func (s *racingStore) DeploysForSite(context.Context, sitekey.Dirname) ([]Deploy, error) {
	s.deploysCalls++
	if s.deploysCalls == s.deploysErrOnCall {
		return nil, errRacingPG
	}
	out := make([]Deploy, 0, len(s.deploys))
	for _, d := range s.deploys {
		out = append(out, d)
	}
	return out, nil
}

func (s *racingStore) AliasTargets(context.Context, sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	s.aliasCalls++
	if s.aliasCalls == s.aliasErrOnCall {
		return nil, time.Time{}, errRacingPG
	}
	return s.aliases, time.Time{}, nil
}

func (s *racingStore) ReindexDeploy(_ context.Context, _ sitekey.Dirname, id string, mtime time.Time, hasMarker bool) (bool, error) {
	if s.tombstones[id] {
		return false, nil
	}
	s.deploys[id] = Deploy{ID: id, Mtime: mtime, HasMarker: hasMarker}
	s.reindexed = append(s.reindexed, id)
	return true, nil
}

func (s *racingStore) RecordTombstone(_ context.Context, _ sitekey.Dirname, id string, _ int64) error {
	s.tombstones[id] = true
	delete(s.deploys, id)
	s.tombstoned = append(s.tombstoned, id)
	return nil
}

func (s *racingStore) PruneDeploy(_ context.Context, _ sitekey.Dirname, id string) error {
	delete(s.deploys, id)
	s.pruned = append(s.pruned, id)
	return nil
}

type racingSession struct {
	inject func()
	locks  int
}

func (s *racingSession) WithSiteLock(ctx context.Context, _ sitekey.Dirname, fn func(context.Context) error) error {
	s.locks++
	if s.inject != nil {
		run := s.inject
		s.inject = nil
		run()
	}
	return fn(ctx)
}

func (s *racingSession) Close(context.Context) {}

type racingLocker struct{ sess *racingSession }

func (l *racingLocker) NewLockSession(context.Context) (LockSession, error) { return l.sess, nil }

func racingReconciler(lister ReconcileLister, store ReconcileStore, mover Mover, sess *racingSession) *Reconciler {
	rc := newReconciler(lister, store, mover)
	rc.Locker = &racingLocker{sess: sess}
	return rc
}

func captureLogs(t *testing.T) *levelCapture {
	t.Helper()
	cap := &levelCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(old) })
	return cap
}

func (h *levelCapture) sawMessage(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.Message == msg {
			return true
		}
	}
	return false
}

func TestReconcile_ConcurrentTombstoneDoesNotResurrectTheIndexRow(t *testing.T) {
	logs := captureLogs(t)
	id := ts(2 * time.Hour)
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{
		"www/deploys/" + id + "/index.html",
		"www/deploys/" + id + "/" + MarkerObjectName,
	}}
	sess := &racingSession{inject: func() {
		require.NoError(t, store.RecordTombstone(context.Background(), "www", id, 0))
	}}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, store.reindexed,
		"a gc-site tombstone committed after the snapshot must block the reindex write")
	assert.Empty(t, report.Reindexed,
		"the report must not claim a reindex the store refused")
	assert.True(t, logs.sawMessage("reconcile.reindex_refused"),
		"the refusal is surfaced, not swallowed")
}

func TestReconcile_DeployIndexedAfterSnapshotIsNotTombstoned(t *testing.T) {
	id := ts(2 * time.Hour)
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{"www/deploys/" + id + "/index.html"}}
	mover := &fakeMover{}
	sess := &racingSession{inject: func() {
		store.deploys[id] = Deploy{ID: id, Mtime: ago(2 * time.Hour)}
	}}

	report, err := racingReconciler(lister, store, mover, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves,
		"a deploy that gained its PG row after the snapshot is live; its bytes must stay put")
	assert.Empty(t, report.OrphanTombstoned)
}

func TestReconcile_MarkerAppearingAfterSnapshotIsNotTombstoned(t *testing.T) {
	id := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + id + "/"
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{
		site:  []string{deployPrefix + "index.html"},
		perID: map[string][]string{deployPrefix: {deployPrefix + "index.html"}},
	}
	mover := &fakeMover{}
	sess := &racingSession{inject: func() {
		lister.perID[deployPrefix] = []string{
			deployPrefix + "index.html",
			deployPrefix + MarkerObjectName,
		}
	}}

	report, err := racingReconciler(lister, store, mover, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves,
		"the deploy finalized between snapshot and lock; its marker forbids the tombstone")
	assert.Empty(t, report.OrphanTombstoned)
}

func TestReconcile_AlreadyTrashedOrphanIsNotMovedTwice(t *testing.T) {
	id := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + id + "/"
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{
		site:  []string{deployPrefix + "index.html"},
		perID: map[string][]string{deployPrefix: {}},
	}
	mover := &fakeMover{}

	report, err := racingReconciler(lister, store, mover, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves, "bytes already moved to trash leave nothing to tombstone")
	assert.Empty(t, store.tombstoned, "no tombstone row for a deploy that has no bytes")
	assert.Empty(t, report.OrphanTombstoned)
}

func TestReconcile_AliasAppearingUnderTheLockBlocksTheTombstone(t *testing.T) {
	id := ts(2 * time.Hour)
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{"www/deploys/" + id + "/index.html"}}
	mover := &fakeMover{}
	sess := &racingSession{inject: func() { store.aliases[id] = struct{}{} }}

	report, err := racingReconciler(lister, store, mover, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves, "an alias taken under the lock pins the deploy (V1)")
	assert.Contains(t, report.AliasedMissing, id, "the raced alias is still reported as drift")
}

func TestReconcile_BytesReappearingUnderTheLockBlockThePrune(t *testing.T) {
	deployPrefix := "www/deploys/ghost/"
	store := &racingStore{
		deploys:    map[string]Deploy{"ghost": {ID: "ghost", Mtime: ago(2 * time.Hour)}},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := keepAlive(store, &racingLister{
		perID: map[string][]string{deployPrefix: {}},
	})
	sess := &racingSession{inject: func() {
		lister.perID[deployPrefix] = []string{deployPrefix + "index.html"}
	}}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, store.pruned,
		"the upload landed after the snapshot; deleting its index row would orphan the bytes")
	assert.Empty(t, report.PGPruned)
}

func TestReconcile_AliasAppearingUnderTheLockBlocksThePrune(t *testing.T) {
	store := &racingStore{
		deploys:    map[string]Deploy{"ghost": {ID: "ghost", Mtime: ago(2 * time.Hour)}},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})
	sess := &racingSession{inject: func() { store.aliases["ghost"] = struct{}{} }}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, store.pruned, "an alias taken under the lock pins the index row")
	assert.Empty(t, report.PGPruned)
}

func TestReconcile_YoungPGRowIsNotPruned(t *testing.T) {
	store := &racingStore{
		deploys:    map[string]Deploy{"fresh": {ID: "fresh", Mtime: ago(5 * time.Minute)}},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := keepAlive(store, &racingLister{})
	sess := &racingSession{}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, report.PGPruned,
		"a row younger than grace belongs to an upload still in flight")
	assert.Zero(t, sess.locks, "a deferred row takes no lock at all")
}

func TestReconcile_BlastCapBoundsDestructiveRepairs(t *testing.T) {
	oldest, middle, newest := ts(5*time.Hour), ts(4*time.Hour), ts(3*time.Hour)
	marked := ts(2 * time.Hour)
	store := &racingStore{
		deploys: map[string]Deploy{
			"ghost-a": {ID: "ghost-a", Mtime: ago(6 * time.Hour)},
			"ghost-b": {ID: "ghost-b", Mtime: ago(6 * time.Hour)},
		},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{
		"www/deploys/" + oldest + "/index.html",
		"www/deploys/" + middle + "/index.html",
		"www/deploys/" + newest + "/index.html",
		"www/deploys/" + marked + "/index.html",
		"www/deploys/" + marked + "/" + MarkerObjectName,
	}}
	rc := racingReconciler(lister, store, &fakeMover{}, &racingSession{})
	rc.BlastCap = 2

	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.True(t, report.Capped)
	assert.Contains(t, report.CapReason, "blast-cap 2")
	assert.Equal(t, []string{oldest, middle}, report.OrphanTombstoned,
		"the cap reaps the oldest orphans first and defers the rest to the next run")
	assert.Empty(t, report.PGPruned, "the cap is spent before the prune list is reached")
	assert.Equal(t, []string{marked}, report.Reindexed,
		"reindex adds no risk and is never capped")
}

func TestReconcile_BlastCapSpillsTheRemainderIntoPrunes(t *testing.T) {
	orphan := ts(5 * time.Hour)
	store := &racingStore{
		deploys: map[string]Deploy{
			"ghost-a": {ID: "ghost-a", Mtime: ago(6 * time.Hour)},
			"ghost-b": {ID: "ghost-b", Mtime: ago(6 * time.Hour)},
		},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{
		site: []string{"www/deploys/" + orphan + "/index.html"},
		perID: map[string][]string{
			"www/deploys/" + orphan + "/": {"www/deploys/" + orphan + "/index.html"},
			"www/deploys/ghost-a/":        {},
			"www/deploys/ghost-b/":        {},
		},
	}
	rc := racingReconciler(lister, store, &fakeMover{}, &racingSession{})
	rc.BlastCap = 2

	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.True(t, report.Capped)
	assert.Equal(t, []string{orphan}, report.OrphanTombstoned)
	assert.Equal(t, []string{"ghost-a"}, report.PGPruned,
		"tombstones are spent first, then the cap remainder goes to prunes")
}

func TestReconcile_DryRunPredictsTheCappedPlan(t *testing.T) {
	oldest, newest := ts(5*time.Hour), ts(4*time.Hour)
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{
		"www/deploys/" + oldest + "/index.html",
		"www/deploys/" + newest + "/index.html",
	}}
	rc := racingReconciler(lister, store, &fakeMover{}, &racingSession{})
	rc.BlastCap = 1

	report, err := rc.ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.True(t, report.Capped, "a dry run must predict the cap the live run will hit")
	assert.Equal(t, []string{oldest, newest}, report.OrphanTombstoned,
		"the report names every drifted deploy and warns separately that a live run would be capped; "+
			"truncating the report to the cap makes the nightly sweep under-report drift, and at cap 0 it "+
			"would report a clean fleet")
	assert.Empty(t, store.tombstoned, "a dry run mutates nothing")
}

func TestReconcile_CancelledContextStopsStartingRepairs(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := racingReconciler(lister, store, mover, &racingSession{}).ReconcileSite(ctx, "www", false)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, mover.moves, "a shutdown must not launch new destructive work")
}

var errRacingPG = errors.New("pg down")

var errRacingR2 = errors.New("r2 down")

func newRacingStore() *racingStore {
	return &racingStore{
		deploys:    map[string]Deploy{},
		aliases:    map[string]struct{}{},
		tombstones: map[string]bool{},
	}
}

func TestReconcile_ReReadDeploysFailureAbortsBeforeTombstone(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	store.deploysErrOnCall = 2
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}

	_, err := racingReconciler(lister, store, mover, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "re-read deploys before tombstone")
	assert.Empty(t, mover.moves, "an unverifiable snapshot must not move bytes")
}

func TestReconcile_ReListFailureAbortsBeforeTombstone(t *testing.T) {
	orphan := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + orphan + "/"
	store := newRacingStore()
	lister := &racingLister{
		site:     []string{deployPrefix + "index.html"},
		perIDErr: map[string]error{deployPrefix: errRacingR2},
	}
	mover := &fakeMover{}

	_, err := racingReconciler(lister, store, mover, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "re-list r2 before tombstone")
	assert.Empty(t, mover.moves)
}

func TestReconcile_ReListFailureAbortsBeforePrune(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	lister := keepAlive(store, &racingLister{
		perIDErr: map[string]error{"www/deploys/ghost/": errRacingR2},
	})

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "re-list r2 before prune")
	assert.Empty(t, store.pruned, "an unverifiable snapshot must not delete an index row")
}

func TestReconcile_ReReadAliasFailureAbortsBeforePrune(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	store.aliasErrOnCall = 2
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "re-read aliases before prune")
	assert.Empty(t, store.pruned)
}

func TestReconcile_CancelledContextStopsBeforeReindex(t *testing.T) {
	marked := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{
		"www/deploys/" + marked + "/index.html",
		"www/deploys/" + marked + "/" + MarkerObjectName,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).ReconcileSite(ctx, "www", false)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, store.reindexed)
}

func TestReconcile_CancelledContextStopsBeforePrune(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).ReconcileSite(ctx, "www", false)

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, store.pruned)
}

func TestReconcile_VerdictReasonsAreDistinct(t *testing.T) {
	assert.Equal(t, "proceed", tombstoneProceed.reason())
	assert.Equal(t, "proceed", pruneProceed.reason())

	seen := map[string]bool{}
	for _, v := range []tombstoneVerdict{
		tombstoneSkipIndexed, tombstoneSkipAliased, tombstoneSkipGone, tombstoneSkipFinalized,
	} {
		assert.NotEqual(t, "proceed", v.reason())
		assert.False(t, seen[v.reason()], "each skip states its own cause in the log")
		seen[v.reason()] = true
	}
	for _, v := range []pruneVerdict{pruneSkipBytesPresent, pruneSkipAliased} {
		assert.NotEqual(t, "proceed", v.reason())
		assert.False(t, seen[v.reason()], "each skip states its own cause in the log")
		seen[v.reason()] = true
	}
}

type ctxProbeMover struct {
	moves    [][2]string
	seenErrs []error
	err      error
}

func (m *ctxProbeMover) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	m.seenErrs = append(m.seenErrs, ctx.Err())
	if m.err != nil {
		return 0, m.err
	}
	m.moves = append(m.moves, [2]string{src, dst})
	return 0, nil
}

type ctxProbeAuditor struct{ seenErrs []error }

func (a *ctxProbeAuditor) AuditTombstone(ctx context.Context, _ sitekey.Dirname, _ string) error {
	a.seenErrs = append(a.seenErrs, ctx.Err())
	return nil
}

func TestReconcile_RepairSurvivesAParentCancelledUnderTheLock(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &ctxProbeMover{}
	auditor := &ctxProbeAuditor{}

	ctx, cancel := context.WithCancel(context.Background())
	sess := &racingSession{inject: cancel}
	rc := racingReconciler(lister, store, mover, sess)
	rc.Audit = auditor

	report, err := rc.ReconcileSite(ctx, "www", false)
	require.NoError(t, err)

	require.Equal(t, []error{nil}, mover.seenErrs,
		"the per-op context is detached, so a cancel under the lock must not abort a half-done move")
	require.Equal(t, []error{nil}, auditor.seenErrs,
		"the audit write is detached too, or the tombstone loses its trail")
	assert.Equal(t, []string{orphan}, report.OrphanTombstoned)
	assert.Equal(t, []string{orphan}, store.tombstoned,
		"the tombstone row must land, or the moved bytes leak with nothing to purge them")
}

func TestReconcile_TombstoneRowFailureLeavesTheBytesUntouched(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &tombstoneFailStore{racingStore: newRacingStore()}
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}

	_, err := racingReconciler(lister, store, mover, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "record orphan")
	assert.Empty(t, mover.moves,
		"bytes only move once a row exists to drive their purge; nothing else ever lists the trash")
}

func TestReconcile_DeferredMoveIsRetriedByTheNextRun(t *testing.T) {
	logs := captureLogs(t)
	orphan := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + orphan + "/"
	store := newRacingStore()
	lister := &racingLister{site: []string{deployPrefix + "index.html"}}
	failing := &ctxProbeMover{err: errRacingR2}

	_, err := racingReconciler(lister, store, failing, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)
	require.ErrorContains(t, err, "tombstone orphan")
	require.True(t, logs.sawMessage("reconcile.tombstone_move_deferred"))
	require.Equal(t, []string{orphan}, store.tombstoned, "the row landed")

	working := &fakeMover{}
	report, err := racingReconciler(lister, store, working, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned,
		"the bytes are still at the deploy prefix, so the next run sees the same drift and completes the move")
	require.Len(t, working.moves, 1)
	assert.Equal(t, "_trash/www/"+orphan+"/", working.moves[0][1])
}

type tombstoneFailStore struct{ *racingStore }

func (s *tombstoneFailStore) RecordTombstone(context.Context, sitekey.Dirname, string, int64) error {
	return errRacingPG
}

func withLiveAliases(rc *Reconciler, ids ...string) *Reconciler {
	live := map[string]struct{}{}
	for _, id := range ids {
		live[id] = struct{}{}
	}
	rc.LiveAliases = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) { return live, nil }
	return rc
}

func TestReconcile_LiveR2AliasBlocksTheTombstoneWhenPostgresForgot(t *testing.T) {
	id := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + id + "/index.html"}}
	mover := &fakeMover{}

	rc := withLiveAliases(racingReconciler(lister, store, mover, &racingSession{}), id)
	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves,
		"R2 holds the only truth about what is live; a lost alias row must not authorise a delete")
	assert.Contains(t, report.AliasedMissing, id)
}

func TestReconcile_LiveR2AliasBlocksThePruneWhenPostgresForgot(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})

	rc := withLiveAliases(racingReconciler(lister, store, &fakeMover{}, &racingSession{}), "ghost")
	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, store.pruned, "a deploy the serving plane still points at keeps its index row")
	assert.Contains(t, report.AliasedMissing, "ghost",
		"an aliased deploy with no bytes is the drift class that pages")
}

func TestReconcile_LiveAliasReadFailureAbortsBeforeTombstone(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}

	rc := racingReconciler(lister, store, mover, &racingSession{})
	rc.LiveAliases = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) { return nil, errRacingR2 }

	_, err := rc.ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "live r2 aliases")
	assert.Empty(t, mover.moves, "an unreadable alias oracle must never authorise a delete")
}

func TestReconcile_LiveAliasReadFailureAbortsBeforePrune(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})

	rc := racingReconciler(lister, store, &fakeMover{}, &racingSession{})
	rc.LiveAliases = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) { return nil, errRacingR2 }

	_, err := rc.ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "live r2 aliases")
	assert.Empty(t, store.pruned)
}

func TestReconcile_LiveRunWithoutLiveAliasesIsRefused(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}

	rc := racingReconciler(lister, store, mover, &racingSession{})
	rc.LiveAliases = nil

	_, err := rc.ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "LiveAliases")
	assert.Empty(t, mover.moves, "a wiring bug must never move bytes on a PG-only alias read")
}

func TestReconcile_ReindexSkipsADeployWhoseBytesVanished(t *testing.T) {
	logs := captureLogs(t)
	id := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + id + "/"
	store := newRacingStore()
	lister := &racingLister{
		site: []string{deployPrefix + "index.html", deployPrefix + MarkerObjectName},
		perID: map[string][]string{
			deployPrefix: {deployPrefix + "index.html", deployPrefix + MarkerObjectName},
		},
	}
	sess := &racingSession{inject: func() { lister.perID[deployPrefix] = []string{} }}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, store.reindexed,
		"indexing a deploy whose bytes are gone would create the very drift the next run has to prune")
	assert.Empty(t, report.Reindexed)
	assert.True(t, logs.sawMessage("reconcile.reindex_refused"))
}

func TestReconcile_ReindexReListFailureAborts(t *testing.T) {
	id := ts(2 * time.Hour)
	deployPrefix := "www/deploys/" + id + "/"
	store := newRacingStore()
	lister := &racingLister{
		site:     []string{deployPrefix + "index.html", deployPrefix + MarkerObjectName},
		perIDErr: map[string]error{deployPrefix: errRacingR2},
	}

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "re-list r2 before reindex")
	assert.Empty(t, store.reindexed)
}

type sessionLocker struct{ sess LockSession }

func (l sessionLocker) NewLockSession(context.Context) (LockSession, error) { return l.sess, nil }

type unlockFailSession struct{ err error }

func (s *unlockFailSession) WithSiteLock(ctx context.Context, _ sitekey.Dirname, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return s.err
}

func (s *unlockFailSession) Close(context.Context) {}

func TestReconcile_UnlockFailureStillRecordsTheCompletedRepair(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	mover := &fakeMover{}
	auditor := &ctxProbeAuditor{}

	rc := newReconciler(lister, store, mover)
	rc.Locker = sessionLocker{sess: &unlockFailSession{err: errors.New("unlock failed")}}
	rc.Audit = auditor

	report, err := rc.ReconcileSite(context.Background(), "www", false)

	require.Error(t, err, "a failed unlock is still an error the caller must see")
	assert.Equal(t, []string{orphan}, store.tombstoned, "the write landed before the unlock failed")
	assert.Equal(t, []string{orphan}, report.OrphanTombstoned,
		"a repair that happened must be reported even when the unlock afterwards failed")
	assert.Len(t, auditor.seenErrs, 1, "and it must still leave an audit row")
}

type pruneRecordingAuditor struct{ ids []string }

func (a *pruneRecordingAuditor) AuditTombstone(_ context.Context, _ sitekey.Dirname, id string) error {
	a.ids = append(a.ids, id)
	return nil
}

func TestReconcile_PruneLeavesAnAuditRow(t *testing.T) {
	store := newRacingStore()
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	lister := keepAlive(store, &racingLister{perID: map[string][]string{"www/deploys/ghost/": {}}})
	auditor := &pruneRecordingAuditor{}

	rc := racingReconciler(lister, store, &fakeMover{}, &racingSession{})
	rc.PruneAudit = auditor

	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	require.Equal(t, []string{"ghost"}, report.PGPruned)
	assert.Equal(t, []string{"ghost"}, auditor.ids,
		"an index row deleted with no trail is a row nobody can account for later")
}

func TestReconcile_PartialReportSurvivesAMidRunFailure(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &tombstoneFailStore{racingStore: newRacingStore()}
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(2 * time.Hour)}
	store.aliases["ghost"] = struct{}{}
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}

	report, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.Error(t, err)
	assert.Equal(t, []string{"ghost"}, report.AliasedMissing,
		"drift found before the failure must still reach the caller, or the run stops paging")
}

func TestReconcile_DryRunAgreesWithTheLiveRunOnLiveAliases(t *testing.T) {
	id := ts(2 * time.Hour)
	newRun := func() *Reconciler {
		store := newRacingStore()
		lister := &racingLister{site: []string{"www/deploys/" + id + "/index.html"}}
		return withLiveAliases(racingReconciler(lister, store, &fakeMover{}, &racingSession{}), id)
	}

	dry, err := newRun().ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)
	live, err := newRun().ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, live.OrphanTombstoned, dry.OrphanTombstoned,
		"a dry run is only useful for sizing the blast radius if it predicts what the live run does")
	assert.Empty(t, dry.OrphanTombstoned, "R2 says the deploy is live, so neither run may plan a tombstone")
	assert.Equal(t, live.AliasedMissing, dry.AliasedMissing)
}

func TestReconcile_RefusedReindexOfAnAliasedDeployStillReportsDrift(t *testing.T) {
	id := ts(2 * time.Hour)
	store := newRacingStore()
	store.aliases[id] = struct{}{}
	lister := &racingLister{site: []string{
		"www/deploys/" + id + "/index.html",
		"www/deploys/" + id + "/" + MarkerObjectName,
	}}
	sess := &racingSession{inject: func() {
		require.NoError(t, store.RecordTombstone(context.Background(), "www", id, 0))
	}}

	report, err := racingReconciler(lister, store, &fakeMover{}, sess).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, report.Reindexed)
	assert.Contains(t, report.AliasedMissing, id,
		"an alias left pointing at a deploy we refused to index is drift nobody else reports")
}

func keepAlive(store *racingStore, lister *racingLister) *racingLister {
	id := ts(2 * time.Hour)
	store.deploys[id] = Deploy{ID: id, Mtime: ago(2 * time.Hour)}
	lister.site = append(lister.site, "www/deploys/"+id+"/index.html")
	return lister
}

func TestReconcile_DeferredMoveStillLeavesAnAuditRow(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := newRacingStore()
	lister := &racingLister{site: []string{"www/deploys/" + orphan + "/index.html"}}
	auditor := &pruneRecordingAuditor{}

	rc := racingReconciler(lister, store, &ctxProbeMover{err: errRacingR2}, &racingSession{})
	rc.Audit = auditor

	_, err := rc.ReconcileSite(context.Background(), "www", false)
	require.ErrorContains(t, err, "tombstone orphan")

	require.Equal(t, []string{orphan}, store.tombstoned)
	assert.Equal(t, []string{orphan}, auditor.ids,
		"the tombstone row is the durable fact, so the trail follows the row and not the byte move")
}
