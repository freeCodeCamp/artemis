package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	plan := PlanSite("www", RetainInput{Deploys: oldDeploys(6, 100), Now: testNow}, testPolicy(), 0)

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

func TestPlanSite_BlastCapDisabled(t *testing.T) {
	plan := PlanSite("www", RetainInput{Deploys: oldDeploys(20, 1), Now: testNow}, testPolicy(), 0)
	assert.False(t, plan.Aborted, "blastCap=0 disables the cap")
	assert.Len(t, plan.Delete, 17)
}
