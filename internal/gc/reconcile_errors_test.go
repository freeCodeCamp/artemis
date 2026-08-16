package gc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errLister struct {
	keys []string
	err  error
}

func (l *errLister) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	return keysUnder(l.keys, prefix), l.err
}

type deepErrStore struct {
	deploys      map[string][]Deploy
	aliases      map[string]struct{}
	deploysErr   error
	aliasErr     error
	reindexErr   error
	tombstoneErr error
	tombstoned   []string
	reindexed    []string
}

func (s *deepErrStore) DeploysForSite(_ context.Context, site string) ([]Deploy, error) {
	return s.deploys[site], s.deploysErr
}

func (s *deepErrStore) AliasTargets(_ context.Context, _ string) (map[string]struct{}, time.Time, error) {
	return s.aliases, time.Time{}, s.aliasErr
}

func (s *deepErrStore) ReindexDeploy(_ context.Context, _, id string, _ time.Time, _ bool) (bool, error) {
	if s.reindexErr != nil {
		return false, s.reindexErr
	}
	s.reindexed = append(s.reindexed, id)
	return true, nil
}

func (s *deepErrStore) RecordTombstone(_ context.Context, _, id string, _ int64) error {
	if s.tombstoneErr != nil {
		return s.tombstoneErr
	}
	s.tombstoned = append(s.tombstoned, id)
	return nil
}

func (s *deepErrStore) PruneDeploy(_ context.Context, _, _ string) error { return nil }

func TestReconcile_ListFailureAborts(t *testing.T) {
	lister := &errLister{err: errors.New("r2 down")}
	store := &deepErrStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}

	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "list r2")
}

func TestReconcile_DeploysForSiteFailureAborts(t *testing.T) {
	lister := &errLister{keys: []string{"www/deploys/" + ts(2*time.Hour) + "/index.html"}}
	store := &deepErrStore{
		deploys:    map[string][]Deploy{},
		aliases:    map[string]struct{}{},
		deploysErr: errors.New("pg down"),
	}

	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "load pg")
}

func TestReconcile_AliasTargetsFailureAborts(t *testing.T) {
	lister := &errLister{keys: []string{"www/deploys/" + ts(2*time.Hour) + "/index.html"}}
	store := &deepErrStore{
		deploys:  map[string][]Deploy{},
		aliases:  map[string]struct{}{},
		aliasErr: errors.New("pg down"),
	}

	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "load aliases")
}

func TestReconcile_ReindexFailureAborts(t *testing.T) {
	completed := ts(2 * time.Hour)
	lister := &errLister{keys: []string{
		"www/deploys/" + completed + "/index.html",
		"www/deploys/" + completed + "/" + MarkerObjectName,
	}}
	store := &deepErrStore{
		deploys:    map[string][]Deploy{},
		aliases:    map[string]struct{}{},
		reindexErr: errors.New("pg down"),
	}

	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "reindex")
	assert.Empty(t, report.Reindexed)
}

func TestReconcile_AliasedReindexFailureAborts(t *testing.T) {
	aliased := ts(2 * time.Hour)
	lister := &errLister{keys: []string{
		"www/deploys/" + aliased + "/index.html",
		"www/deploys/" + aliased + "/" + MarkerObjectName,
	}}
	store := &deepErrStore{
		deploys:    map[string][]Deploy{},
		aliases:    map[string]struct{}{aliased: {}},
		reindexErr: errors.New("pg down"),
	}

	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "reindex "+aliased)
}

func TestReconcile_MoveFailureLeavesARetryableTombstoneRow(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &errLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &deepErrStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &errMover{err: errors.New("r2 down")}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "tombstone orphan")
	assert.Empty(t, report.OrphanTombstoned, "an unmoved deploy is never reported as tombstoned")
	assert.Equal(t, []string{orphan}, store.tombstoned,
		"the row lands first, so a failed move leaves bytes at the deploy prefix where the next run finds them again")
}

func TestReconcile_RecordTombstoneFailureAborts(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &errLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &deepErrStore{
		deploys:      map[string][]Deploy{},
		aliases:      map[string]struct{}{},
		tombstoneErr: errors.New("pg down"),
	}

	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "record orphan")
}

func TestReconcile_KeyWithNoDeploySegmentIsIgnored(t *testing.T) {
	lister := &errLister{keys: []string{"www/deploys/"}}
	store := &deepErrStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}

	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)

	require.NoError(t, err)
	assert.Empty(t, report.Reindexed)
	assert.Empty(t, report.OrphanTombstoned)
}
