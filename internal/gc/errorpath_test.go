package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errMover struct {
	err   error
	moves [][2]string
}

func (m *errMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	m.moves = append(m.moves, [2]string{src, dst})
	return 0, m.err
}

func expiredOne() []Tombstone {
	return []Tombstone{{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour), Bytes: 100}}
}

type errDeleter struct {
	err     error
	deleted []string
}

func (d *errDeleter) DeletePrefix(_ context.Context, prefix string) (int, error) {
	d.deleted = append(d.deleted, prefix)
	return 0, d.err
}

type errClearReaper struct {
	*fakeReaper
	clearErr error
}

func (r *errClearReaper) ClearTombstone(ctx context.Context, site, id string) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	return r.fakeReaper.ClearTombstone(ctx, site, id)
}

func newErrPurge(reaper TombstoneReaper, del Deleter) *TombstonePurge {
	return &TombstonePurge{
		Store:     reaper,
		Deleter:   del,
		Recovery:  7 * 24 * time.Hour,
		TrashBase: "_trash/",
		Now:       func() time.Time { return testNow },
	}
}

type errReconcileStore struct {
	deploys        map[string][]Deploy
	aliases        map[string]struct{}
	aliasErrOnCall int
	aliasCalls     int
	pruneErr       error
	tombstoned     []string
	pruned         []string
}

func (s *errReconcileStore) DeploysForSite(_ context.Context, site string) ([]Deploy, error) {
	return s.deploys[site], nil
}

func (s *errReconcileStore) AliasTargets(_ context.Context, _ string) (map[string]struct{}, time.Time, error) {
	s.aliasCalls++
	if s.aliasErrOnCall != 0 && s.aliasCalls == s.aliasErrOnCall {
		return nil, time.Time{}, errors.New("pg read failed")
	}
	return s.aliases, time.Time{}, nil
}

func (s *errReconcileStore) UpsertDeploy(_ context.Context, _, _ string, _ time.Time, _ int64, _ bool, _ string) error {
	return nil
}

func (s *errReconcileStore) RecordTombstone(_ context.Context, _, id string, _ int64) error {
	s.tombstoned = append(s.tombstoned, id)
	return nil
}

func (s *errReconcileStore) PruneDeploy(_ context.Context, _, id string) error {
	if s.pruneErr != nil {
		return s.pruneErr
	}
	s.pruned = append(s.pruned, id)
	return nil
}

func TestGC_TombstoneRecordFailurePropagates(t *testing.T) {
	store := &fakeStore{
		deploys:      map[string][]Deploy{"www": sixOld()},
		targetsSeq:   []map[string]struct{}{{}},
		tombstoneErr: errors.New("pg down"),
	}
	mover := &fakeMover{}

	res, err := newSiteGC(store, mover).Run(context.Background(), "www", false)

	require.ErrorContains(t, err, "record tombstone")
	assert.Empty(t, res.Tombstoned, "a failed PG tombstone is not reported as reclaimed")
	assert.EqualValues(t, 0, res.BytesReclaimed, "no bytes accounted for an unrecorded tombstone")
	require.Len(t, mover.moves, 1, "the R2 move ran before the PG write failed, leaving orphaned bytes the retry must reclaim")
	assert.Empty(t, store.tombstoned)
}

func TestGC_MoveFailureAbortsBeforeTombstone(t *testing.T) {
	mover := &errMover{err: errors.New("r2 5xx")}
	store := &fakeStore{
		deploys:    map[string][]Deploy{"www": sixOld()},
		targetsSeq: []map[string]struct{}{{}},
	}

	res, err := newSiteGC(store, mover).Run(context.Background(), "www", false)

	require.ErrorContains(t, err, "tombstone-move")
	assert.Empty(t, store.tombstoned, "no PG tombstone when the R2 move failed (V1/V5)")
	assert.Empty(t, res.Tombstoned)
	require.Len(t, mover.moves, 1, "aborts on the first failed move, never proceeding to the next deploy")
}

func TestTombstonePurge_ClearFailurePersistsRowForRetry(t *testing.T) {
	reaper := &errClearReaper{
		fakeReaper: &fakeReaper{tombstones: expiredOne()},
		clearErr:   errors.New("pg down"),
	}
	del := &fakeDeleter{}

	_, err := newErrPurge(reaper, del).Run(context.Background(), false)

	require.ErrorContains(t, err, "clear")
	assert.Equal(t, []string{"_trash/www/d-expired/"}, del.deleted, "R2 delete still happened; row left for idempotent retry (V10)")
	assert.Len(t, reaper.tombstones, 1, "the tombstone row survives a failed clear so a re-run safely re-deletes")
}

func TestTombstonePurge_DeleteFailureAbortsBeforeClear(t *testing.T) {
	del := &errDeleter{err: errors.New("r2 down")}
	reaper := &fakeReaper{tombstones: expiredOne()}

	res, err := newErrPurge(reaper, del).Run(context.Background(), false)

	require.ErrorContains(t, err, "delete")
	assert.Empty(t, reaper.cleared, "PG row not cleared when R2 delete failed")
	assert.Empty(t, res.Purged, "nothing reported reclaimed when the R2 delete failed")
	assert.EqualValues(t, 0, res.BytesReclaimed)
}

func TestReconcile_ReReadAliasFailureAbortsBeforeTombstone(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &errReconcileStore{
		deploys:        map[string][]Deploy{},
		aliases:        map[string]struct{}{},
		aliasErrOnCall: 2,
	}
	mover := &errMover{}

	_, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www")

	require.ErrorContains(t, err, "re-read aliases before tombstone")
	assert.Empty(t, mover.moves, "no move when the safety re-read failed (V1)")
	assert.Empty(t, store.tombstoned, "no tombstone recorded when the re-read errored")
}

func TestReconcile_PruneFailurePropagates(t *testing.T) {
	lister := &fakeReconcileLister{keys: nil}
	store := &errReconcileStore{
		deploys:  map[string][]Deploy{"www": {{ID: "ghost", Mtime: ago(time.Hour)}}},
		aliases:  map[string]struct{}{},
		pruneErr: errors.New("pg down"),
	}

	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www")

	require.ErrorContains(t, err, "prune ghost")
	assert.Empty(t, report.PGPruned, "a failed PruneDeploy is not reported as pruned")
	assert.Empty(t, store.pruned)
}
