package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type recordingLockSession struct {
	sites     []string
	closed    bool
	lockErr   error
	underLock bool
}

func (s *recordingLockSession) WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error {
	if s.lockErr != nil {
		return s.lockErr
	}
	s.sites = append(s.sites, string(site))
	s.underLock = true
	defer func() { s.underLock = false }()
	return fn(ctx)
}

func (s *recordingLockSession) Close(context.Context) { s.closed = true }

type recordingLocker struct {
	sess    *recordingLockSession
	openErr error
}

func (l *recordingLocker) NewLockSession(context.Context) (LockSession, error) {
	if l.openErr != nil {
		return nil, l.openErr
	}
	return l.sess, nil
}

type lockAwareStore struct {
	sess       *recordingLockSession
	deploys    map[string][]Deploy
	aliases    map[string]struct{}
	tombstoned []string
	reindexed  []string
	pruned     []string
	unlocked   []string
}

func (s *lockAwareStore) note(op string) {
	if s.sess != nil && !s.sess.underLock {
		s.unlocked = append(s.unlocked, op)
	}
}

func (s *lockAwareStore) DeploysForSite(_ context.Context, site sitekey.Dirname) ([]Deploy, error) {
	return s.deploys[string(site)], nil
}

func (s *lockAwareStore) AliasTargets(context.Context, sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	return s.aliases, time.Time{}, nil
}

func (s *lockAwareStore) ReindexDeploy(_ context.Context, _ sitekey.Dirname, id string, _ time.Time, _ bool) (bool, error) {
	s.note("ReindexDeploy")
	s.reindexed = append(s.reindexed, id)
	return true, nil
}

func (s *lockAwareStore) RecordTombstone(_ context.Context, _ sitekey.Dirname, id string, _ int64) error {
	s.note("RecordTombstone")
	s.tombstoned = append(s.tombstoned, id)
	return nil
}

func (s *lockAwareStore) PruneDeploy(_ context.Context, _ sitekey.Dirname, id string) error {
	s.note("PruneDeploy")
	s.pruned = append(s.pruned, id)
	return nil
}

type lockAwareMover struct {
	sess     *recordingLockSession
	moves    [][2]string
	unlocked []string
}

func (m *lockAwareMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	if m.sess != nil && !m.sess.underLock {
		m.unlocked = append(m.unlocked, src)
	}
	m.moves = append(m.moves, [2]string{src, dst})
	return 0, nil
}

func lockedReconciler(lister ReconcileLister, store *lockAwareStore, mover *lockAwareMover, locker Locker) *Reconciler {
	rc := newReconciler(lister, store, mover)
	rc.Locker = locker
	return rc
}

func TestReconcile_TombstonesUnderTheSiteLock(t *testing.T) {
	orphan := ts(2 * time.Hour)
	sess := &recordingLockSession{}
	store := &lockAwareStore{sess: sess, deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{sess: sess}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}

	report, err := lockedReconciler(lister, store, mover, &recordingLocker{sess: sess}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned)
	assert.Equal(t, []string{"www"}, sess.sites, "the destructive move must run inside a site lock")
	assert.Empty(t, mover.unlocked, "no byte move may happen outside the lock")
	assert.Empty(t, store.unlocked, "no row write may happen outside the lock")
	assert.True(t, sess.closed, "the lock session must be closed")
}

func TestReconcile_PrunesUnderTheSiteLock(t *testing.T) {
	gone := ts(2 * time.Hour)
	sess := &recordingLockSession{}
	keeper := ts(3 * time.Hour)
	store := &lockAwareStore{
		sess:    sess,
		deploys: map[string][]Deploy{"www": {{ID: gone}, {ID: keeper, Mtime: ago(3 * time.Hour)}}},
		aliases: map[string]struct{}{},
	}
	mover := &lockAwareMover{sess: sess}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + keeper + "/index.html"}}

	report, err := lockedReconciler(lister, store, mover, &recordingLocker{sess: sess}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{gone}, report.PGPruned)
	assert.Empty(t, store.unlocked, "a prune deletes a row and must hold the lock")
}

func TestReconcile_ReindexUnderTheSiteLock(t *testing.T) {
	completed := ts(2 * time.Hour)
	sess := &recordingLockSession{}
	store := &lockAwareStore{sess: sess, deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{sess: sess}
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/" + completed + "/index.html",
		"www/deploys/" + completed + "/" + MarkerObjectName,
	}}

	report, err := lockedReconciler(lister, store, mover, &recordingLocker{sess: sess}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{completed}, report.Reindexed)
	assert.Empty(t, store.unlocked, "a reindex writes a row and must hold the lock")
}

func TestReconcile_DryRunNeedsNoLock(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &lockAwareStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned,
		"a dry run reports drift without a lock because it mutates nothing")
}

func TestReconcile_LiveRunWithoutLockerIsRefused(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &lockAwareStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}

	rc := newReconciler(lister, store, mover)
	rc.Locker = nil

	_, err := rc.ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "Locker")
	assert.Empty(t, mover.moves, "a wiring bug must never move bytes unserialized")
}

func TestReconcile_LockSessionOpenFailureAborts(t *testing.T) {
	orphan := ts(2 * time.Hour)
	store := &lockAwareStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	locker := &recordingLocker{openErr: errors.New("pg down")}

	_, err := lockedReconciler(lister, store, mover, locker).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "lock session")
	assert.Empty(t, mover.moves)
}

func TestReconcile_ContendedLockAbortsWithoutMutating(t *testing.T) {
	orphan := ts(2 * time.Hour)
	sess := &recordingLockSession{lockErr: errors.New("lock timeout")}
	store := &lockAwareStore{sess: sess, deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &lockAwareMover{sess: sess}
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}

	_, err := lockedReconciler(lister, store, mover, &recordingLocker{sess: sess}).
		ReconcileSite(context.Background(), "www", false)

	require.Error(t, err)
	assert.Empty(t, mover.moves, "a contended site must not be reconciled destructively")
}
