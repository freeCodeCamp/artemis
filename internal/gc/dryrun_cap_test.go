package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileSite_DryRunReportsFullDriftRegardlessOfTheBlastCap(t *testing.T) {
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/20260101-000000-aaaaaaa/index.html",
		"www/deploys/20260102-000000-bbbbbbb/index.html",
		"www/deploys/20260103-000000-ccccccc/index.html",
	}}
	store := &fakeReconcileStore{}
	rc := newReconciler(lister, store, &fakeMover{})
	rc.BlastCap = 0
	rc.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }

	report, err := rc.ReconcileSite(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Len(t, report.OrphanTombstoned, 3,
		"the nightly sweep runs with the same cap as repair; letting the cap empty the REPORT turns a "+
			"misconfigured ceiling into a false all-clear, which is worse than the drift it hides")
}
