package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcile_EmptyListingNeverAuthorisesWipingTheIndex(t *testing.T) {
	store := newRacingStore()
	store.deploys["d1"] = Deploy{ID: "d1", Mtime: ago(30 * 24 * time.Hour)}
	store.deploys["d2"] = Deploy{ID: "d2", Mtime: ago(30 * 24 * time.Hour)}
	lister := &racingLister{site: []string{}}

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.ErrorContains(t, err, "returned no deploy prefix: refusing to treat an empty listing as total drift",
		"an R2 prefix that matches nothing is how the keyspace bug looked; it must never read as 'delete it all', "+
			"and the refusal must stay unconditional: a discriminator that excuses the empty listing reads "+
			"'... no deploy prefix AND <excuse>: refusing ...' and fails this contiguous match")
	require.ErrorContains(t, err, "2 active deploys")
	assert.Empty(t, store.pruned, "not one index row may be deleted on the strength of an empty listing")
}

func TestReconcile_EmptyListingIsReportedByADryRunToo(t *testing.T) {
	store := newRacingStore()
	store.deploys["d1"] = Deploy{ID: "d1", Mtime: ago(30 * 24 * time.Hour)}
	lister := &racingLister{site: []string{}}

	_, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", true)

	require.Error(t, err,
		"a dry run that hides the fault would let an operator arm the reconciler against it")
}

func TestReconcile_EmptySiteWithAnEmptyIndexIsNotAFault(t *testing.T) {
	store := newRacingStore()
	lister := &racingLister{site: []string{}}

	report, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)

	require.NoError(t, err, "a site with no bytes and no rows is genuinely empty, not broken")
	assert.Empty(t, report.PGPruned)
}

func TestReconcile_OneSurvivingPrefixStillAllowsPruningTheRest(t *testing.T) {
	alive := ts(30 * 24 * time.Hour)
	store := newRacingStore()
	store.deploys[alive] = Deploy{ID: alive, Mtime: ago(30 * 24 * time.Hour)}
	store.deploys["ghost"] = Deploy{ID: "ghost", Mtime: ago(30 * 24 * time.Hour)}
	lister := &racingLister{
		site:  []string{"www/deploys/" + alive + "/index.html"},
		perID: map[string][]string{"www/deploys/ghost/": {}},
	}

	report, err := racingReconciler(lister, store, &fakeMover{}, &racingSession{}).
		ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{"ghost"}, report.PGPruned,
		"the guard is about a listing that returned nothing at all, not about ordinary per-deploy drift")
}
