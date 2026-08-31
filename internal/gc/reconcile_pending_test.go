package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestReconcile_PendingDeployIsNotAnOrphan(t *testing.T) {
	pending := ts(72 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"latex/deploys/" + pending + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	rc := newReconciler(lister, store, mover)
	rc.PendingIDs = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
		return map[string]struct{}{pending: {}}, nil
	}

	rep, err := rc.ReconcileSite(context.Background(), "latex", false)
	require.NoError(t, err)

	assert.Empty(t, store.tombstoned,
		"artemis recorded this deploy as pending, so it is owned; tombstoning it as ownerless drift is the B5 defect")
	assert.Empty(t, rep.OrphanTombstoned)
	assert.Empty(t, mover.moves, "a pending deploy's bytes must stay where the uploader is still writing them")
}

func TestReconcile_PendingLookupFailureAborts(t *testing.T) {
	pending := ts(72 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"latex/deploys/" + pending + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	rc := newReconciler(lister, store, mover)
	rc.PendingIDs = func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
		return nil, assert.AnError
	}

	_, err := rc.ReconcileSite(context.Background(), "latex", false)
	require.Error(t, err, "an unreadable pending set must abort rather than tombstone blind")
	assert.Empty(t, store.tombstoned)
	assert.Empty(t, mover.moves)
}

func TestReconcile_NilPendingIDsKeepsOldBehaviour(t *testing.T) {
	orphan := ts(72 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"latex/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}

	rc := newReconciler(lister, store, &fakeMover{})
	rc.PendingIDs = nil

	rep, err := rc.ReconcileSite(context.Background(), "latex", false)
	require.NoError(t, err)
	assert.Equal(t, []string{orphan}, rep.OrphanTombstoned,
		"with no pending reader wired, a genuine orphan must still be tombstoned")
}
