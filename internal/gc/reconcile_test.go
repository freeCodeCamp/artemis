package gc

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReconcileStore struct {
	deploys    map[string][]Deploy
	aliases    map[string]struct{}
	reindexed  []string
	tombstoned []string
	pruned     []string
}

func (s *fakeReconcileStore) DeploysForSite(_ context.Context, site string) ([]Deploy, error) {
	return s.deploys[site], nil
}
func (s *fakeReconcileStore) AliasTargets(_ context.Context, _ string) (map[string]struct{}, time.Time, error) {
	return s.aliases, time.Time{}, nil
}
func (s *fakeReconcileStore) UpsertDeploy(_ context.Context, _, id string, _ time.Time, _ int64, _ bool, _ string) error {
	s.reindexed = append(s.reindexed, id)
	return nil
}
func (s *fakeReconcileStore) RecordTombstone(_ context.Context, _, id string, _ int64) error {
	s.tombstoned = append(s.tombstoned, id)
	return nil
}
func (s *fakeReconcileStore) PruneDeploy(_ context.Context, _, id string) error {
	s.pruned = append(s.pruned, id)
	return nil
}

func newReconciler(lister ReconcileLister, store ReconcileStore, mover Mover) *Reconciler {
	return &Reconciler{
		Lister:       lister,
		Store:        store,
		Mover:        mover,
		Grace:        time.Hour,
		SitePrefix:   func(site string) string { return site + "/deploys/" },
		DeployPrefix: func(site, id string) string { return site + "/deploys/" + id + "/" },
		TrashPrefix:  func(site, id string) string { return "_trash/" + site + "/" + id + "/" },
		Now:          func() time.Time { return testNow },
	}
}

type fakeReconcileLister struct{ keys []string }

func (f *fakeReconcileLister) ListPrefix(context.Context, string) ([]string, error) {
	return f.keys, nil
}

func ts(d time.Duration) string {
	return testNow.Add(-d).UTC().Format("20060102-150405") + "-sha1234"
}

func TestReconcile_Orphan(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned, "no-marker, past-grace, unindexed R2 prefix -> tombstoned (E4)")
	assert.Equal(t, []string{orphan}, store.tombstoned)
	require.Len(t, mover.moves, 1)
}

func TestReconcile_Rebuild(t *testing.T) {
	completed := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/" + completed + "/index.html",
		"www/deploys/" + completed + "/" + MarkerObjectName,
	}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)

	assert.Equal(t, []string{completed}, report.Reindexed, "marked-complete R2 deploy missing from PG -> re-indexed (E3)")
	assert.Empty(t, report.OrphanTombstoned, "a completed deploy is never tombstoned by reconcile")
	assert.Empty(t, mover.moves)
}

func TestReconcile_InflightSkipped(t *testing.T) {
	young := ts(5 * time.Minute)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + young + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)
	assert.Empty(t, report.OrphanTombstoned, "no-marker but within grace -> in-flight, left alone")
	assert.Empty(t, store.tombstoned)
}

func TestReconcile_PrunesStalePGRow(t *testing.T) {
	lister := &fakeReconcileLister{keys: []string{}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {{ID: "ghost", Mtime: ago(time.Hour)}}},
		aliases: map[string]struct{}{},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)
	assert.Equal(t, []string{"ghost"}, report.PGPruned, "PG row with no R2 bytes pruned")
}

func TestReconcile_AliasedMissingNotPruned(t *testing.T) {
	lister := &fakeReconcileLister{keys: []string{}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {{ID: "live", Mtime: ago(time.Hour)}}},
		aliases: map[string]struct{}{"live": {}},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)
	assert.Empty(t, report.PGPruned, "an aliased deploy whose bytes vanished is alerted, never silently pruned")
	assert.Equal(t, []string{"live"}, report.AliasedMissing)
}

func TestReconcile_AliasedOrphanNotTombstoned(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + id + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{},
		aliases: map[string]struct{}{id: {}},
	}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)

	assert.NotContains(t, report.OrphanTombstoned, id,
		"an alias-pinned deploy is never tombstoned even when unindexed + marker-less + past grace (V1)")
	assert.Empty(t, mover.moves, "no R2 move of an aliased deploy")
	assert.Empty(t, store.tombstoned)
	assert.Contains(t, report.AliasedMissing, id, "surfaced as drift to alert on instead")
}

func TestReconcile_DriftMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{"www": {{ID: "ghost", Mtime: ago(time.Hour)}}}, aliases: map[string]struct{}{}}
	rc := newReconciler(lister, store, &fakeMover{})
	rc.Metrics = m

	_, err := rc.ReconcileSite(context.Background(), "www")
	require.NoError(t, err)
	assert.EqualValues(t, 1, testutil.ToFloat64(m.Drift.WithLabelValues("orphan")))
	assert.EqualValues(t, 1, testutil.ToFloat64(m.Drift.WithLabelValues("pruned")))
}

func TestReconcile_ConsistentNoDrift(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + id + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {{ID: id, Mtime: ago(2 * time.Hour)}}},
		aliases: map[string]struct{}{},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www")
	require.NoError(t, err)
	assert.Empty(t, report.Reindexed)
	assert.Empty(t, report.OrphanTombstoned)
	assert.Empty(t, report.PGPruned)
}
