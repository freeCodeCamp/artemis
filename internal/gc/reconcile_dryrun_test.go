package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileDryRun_ReportsOrphanWithoutMovingBytes(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned)
	assert.Empty(t, mover.moves, "dry run must move no bytes")
	assert.Empty(t, store.tombstoned, "dry run must record no tombstone")
}

func TestReconcileDryRun_ReportsReindexWithoutWritingIndex(t *testing.T) {
	completed := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/" + completed + "/index.html",
		"www/deploys/" + completed + "/" + MarkerObjectName,
	}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Equal(t, []string{completed}, report.Reindexed)
	assert.Empty(t, store.reindexed, "dry run must write no index row")
}

func TestReconcileDryRun_ReportsPruneWithoutDeletingRow(t *testing.T) {
	gone := ts(2 * time.Hour)
	keeper := ts(3 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + keeper + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {{ID: gone}, {ID: keeper, Mtime: ago(3 * time.Hour)}}},
		aliases: map[string]struct{}{},
	}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Equal(t, []string{gone}, report.PGPruned)
	assert.Empty(t, store.pruned, "dry run must delete no deploy row")
}

func TestReconcileWetRun_StillMutates(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned)
	assert.Len(t, mover.moves, 1, "wet run still moves bytes")
	assert.Equal(t, []string{orphan}, store.tombstoned)
}
