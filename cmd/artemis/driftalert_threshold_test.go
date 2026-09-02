package main

import (
	"testing"

	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sweepWithReclaimable(reindex, tombstone int) sweepResult {
	var s siteDrift
	s.Site = "www.freecode.camp"
	for i := 0; i < reindex; i++ {
		s.Reindex = append(s.Reindex, "r")
	}
	for i := 0; i < tombstone; i++ {
		s.Tombstone = append(s.Tombstone, "t")
	}
	return sweepResult{
		Reports: []siteDrift{s},
		Stats:   sweepStats{Sites: 1, PGDeploys: 1, IndexedTotal: 1, R2Objects: 1},
	}
}

func TestClassifyDrift_AlertsOnceReclaimableDriftAccumulates(t *testing.T) {
	t.Parallel()

	v := classifyDrift(sweepWithReclaimable(reclaimableAlertThreshold, 0))

	require.Equal(t, opDriftReclaimable, v.Op,
		"abandoned deploy sessions accrued for 33 days with nothing watching; a report-only cron that never "+
			"raises on them is indistinguishable from not looking")
	assert.False(t, v.Fails, "reclaimable drift is storage cost, not an outage: alert, do not fail the run")
	require.Error(t, v.Err)
	assert.Contains(t, v.Err.Error(), "artemis reconcile")
	assert.Contains(t, v.Err.Error(), "www.freecode.camp",
		"a fleet-wide count with a literal <site> placeholder tells the operator nothing about where to look")
}

func TestClassifyDrift_AliasedMissingOutranksTheReclaimableThreshold(t *testing.T) {
	t.Parallel()

	res := sweepWithReclaimable(reclaimableAlertThreshold+50, 0)
	res.Reports[0].Aliased = []string{"d1"}

	v := classifyDrift(res)

	assert.Equal(t, opDriftAliasedMissing, v.Op,
		"a live site serving nothing must not be masked by a large but harmless reclaimable count")
}

func TestEveryDriftVerdictOpIsCronShaped(t *testing.T) {
	t.Parallel()

	for _, op := range []string{opDriftSweep, opDriftSelfCheck, opDriftUnreadable, opDriftAliasedMissing, opDriftReclaimable, opDriftOrphanAliases, opDriftLedger} {
		assert.True(t, observability.IsCronShaped(op),
			"op %s bypasses the transient-rate tracker only if cronShapedOps lists it; this test lives "+
				"beside the op constants so adding a sixth verdict here fails until the map learns it — "+
				"the observability-side test could only restate the map against itself", op)
	}
}

func TestClassifyDrift_AlertsOnASingleReclaimableDeploy(t *testing.T) {
	t.Parallel()

	v := classifyDrift(sweepWithReclaimable(1, 0))

	require.Equal(t, opDriftReclaimable, v.Op,
		"the 2026-08-29 drain measured the floor at zero and the nightlies of 08-29, 08-30 and 08-31 all "+
			"reported reclaimable=0, so one reclaimable deploy is an anomaly, not residue; the old threshold "+
			"of 25 was picked to sit under a standing backlog of 36 and could never detect a new orphan")
	assert.False(t, v.Fails, "reclaimable drift is storage cost, not an outage: alert, do not fail the run")
}

func TestClassifyDrift_StaysQuietOnACleanSweep(t *testing.T) {
	t.Parallel()

	v := classifyDrift(sweepWithReclaimable(0, 0))

	assert.Empty(t, v.Op, "a sweep that finds nothing reclaimable must raise nothing")
}
