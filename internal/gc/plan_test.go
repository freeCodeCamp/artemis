package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func oldDeploys(n int, eachBytes int64) []Deploy {
	out := make([]Deploy, n)
	for i := range out {
		out[i] = Deploy{
			ID:        string(rune('a'+i)) + "-old",
			Mtime:     ago(time.Duration(30+i) * 24 * time.Hour),
			Bytes:     eachBytes,
			HasMarker: true,
		}
	}
	return out
}

func TestPlanSite_KeepN(t *testing.T) {
	plan := PlanSite("www", RetainInput{Deploys: oldDeploys(6, 100), Now: testNow}, testPolicy(), defaultTestBlastCap)

	assert.Len(t, plan.Delete, 3, "6 old deploys, keepN=3 -> 3 deletable (V2)")
	assert.False(t, plan.Aborted)
	assert.EqualValues(t, 300, plan.TotalBytes, "bytes summed across delete set")
}

func TestGC_BlastCap(t *testing.T) {
	under := PlanSite("www", RetainInput{Deploys: oldDeploys(6, 10), Now: testNow}, testPolicy(), 5)
	assert.False(t, under.Aborted, "3 deletes under cap=5")
	assert.Len(t, under.Delete, 3)

	over := PlanSite("www", RetainInput{Deploys: oldDeploys(10, 10), Now: testNow}, testPolicy(), 5)
	assert.True(t, over.Aborted, "7 deletes over cap=5 -> capped (partial progress, not total abort)")
	assert.Len(t, over.Delete, 5, "capped plan reaps exactly blast-cap deploys")
	assert.EqualValues(t, 50, over.TotalBytes, "bytes summed across the capped delete set")
	assert.Contains(t, over.Reason, "blast-cap")

	ids := map[string]bool{}
	for _, d := range over.Delete {
		ids[d.ID] = true
	}
	assert.True(t, ids["j-old"], "oldest deletable deploy is reaped first under the cap")
	assert.False(t, ids["d-old"], "newest deletable deploy is spared until a later run")
}

func TestPlanSite_BlastCapZeroRefusesEveryDelete(t *testing.T) {
	plan := PlanSite("www", RetainInput{Deploys: oldDeploys(20, 1), Now: testNow}, testPolicy(), 0)
	assert.True(t, plan.Aborted,
		"an unset cap once meant unlimited, so an environment that forgot CLEANUP_BLAST_CAP reaped a whole "+
			"site in one run")
	assert.Empty(t, plan.Delete)
	assert.Contains(t, plan.Reason, "blast-cap 0")
}

func planIDs(p Plan) []string {
	out := make([]string, 0, len(p.Delete))
	for _, d := range p.Delete {
		out = append(out, d.ID)
	}
	return out
}

func TestPlanSite_ExpiredPendingJoinsTheDeleteSetUnderTheSameCap(t *testing.T) {
	in := RetainInput{
		Deploys: oldDeploys(6, 100),
		Expired: []Deploy{{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
		Now:     testNow,
	}

	plan := PlanSite("www", in, testPolicy(), 2)

	assert.True(t, plan.Aborted, "3 retention deletes plus 1 expired pending exceeds a cap of 2")
	assert.Len(t, plan.Delete, 2,
		"abandoned sessions must be bounded by the same ceiling as retention, not appended past it")
}

func TestPlanSite_CapReapsTheOldestAcrossBothDeleteSources(t *testing.T) {
	in := RetainInput{
		Deploys: oldDeploys(4, 100),
		Expired: []Deploy{
			{ID: "pend-newest", Mtime: testNow.Add(-73 * time.Hour)},
			{ID: "pend-oldest", Mtime: testNow.Add(-500 * time.Hour)},
		},
		Now: testNow,
	}

	plan := PlanSite("www", in, testPolicy(), 2)

	require.True(t, plan.Aborted)
	assert.Equal(t, []string{"d-old", "pend-oldest"}, planIDs(plan),
		"Retain returns newest-first while ExpiredPendingDeploys returns oldest-first, and the cap slices "+
			"the tail; concatenating them unsorted makes the tail the NEWEST abandoned sessions while the "+
			"reason string still claims it reaped the oldest, starving retention on any site over the cap")
}
