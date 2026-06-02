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
	assert.True(t, over.Aborted, "7 deletes over cap=5 -> abort (V6)")
	assert.Empty(t, over.Delete, "aborted plan deletes nothing")
	assert.EqualValues(t, 0, over.TotalBytes)
	assert.Contains(t, over.Reason, "blast-cap")
}

func TestPlanSite_BlastCapDisabled(t *testing.T) {
	plan := PlanSite("www", RetainInput{Deploys: oldDeploys(20, 1), Now: testNow}, testPolicy(), 0)
	assert.False(t, plan.Aborted, "blastCap=0 disables the cap")
	assert.Len(t, plan.Delete, 17)
}
