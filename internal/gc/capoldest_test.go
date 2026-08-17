package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlastCap_BothPathsSelectTheSameOldestSurvivors(t *testing.T) {
	ids := []string{
		"20260101-000000-aaaaaaa",
		"20260201-000000-bbbbbbb",
		"20260301-000000-ccccccc",
		"20260401-000000-ddddddd",
	}
	mt := func(id string) time.Time {
		ts, err := time.Parse("20060102-150405", id[:15])
		require.NoError(t, err)
		return ts
	}

	deploys := make([]Deploy, 0, len(ids))
	for _, id := range ids {
		deploys = append(deploys, Deploy{ID: id, Mtime: mt(id)})
	}
	plan := PlanSite("www", RetainInput{Deploys: deploys, Now: mt(ids[3]).Add(365 * 24 * time.Hour)},
		Policy{RecentKeep: 0, Grace: time.Hour, Retention: time.Hour}, 2)
	require.True(t, plan.Aborted)

	rc := &Reconciler{BlastCap: 2}
	rp := &repairPlan{tombstone: append([]string(nil), ids...)}
	rep := &DriftReport{}
	rc.applyBlastCap(context.Background(), "www", rp, rep)
	require.True(t, rep.Capped)

	assert.Equal(t, []string{ids[0], ids[1]}, planIDs(plan),
		"retention GC and reconcile advertise the same contract — an over-cap run reaps the oldest N — "+
			"so given identical candidates they must pick identical survivors")
	assert.Equal(t, []string{ids[0], ids[1]}, rp.tombstone,
		"one path once sorted DESC and took the tail while the other sorted ASC and took the head; the "+
			"selection must live in exactly one helper so the conventions cannot diverge again")
}

func TestApplyBlastCap_TombstonesConsumeTheBudgetBeforePrunes(t *testing.T) {
	rc := &Reconciler{BlastCap: 3}
	rp := &repairPlan{
		tombstone: []string{"20260101-000000-aaaaaaa", "20260201-000000-bbbbbbb"},
		prune:     []string{"20260102-000000-xxxxxxx", "20260202-000000-yyyyyyy"},
	}
	rep := &DriftReport{}

	rc.applyBlastCap(context.Background(), "www", rp, rep)

	require.True(t, rep.Capped)
	assert.Equal(t, []string{"20260101-000000-aaaaaaa", "20260201-000000-bbbbbbb"}, rp.tombstone)
	assert.Equal(t, []string{"20260102-000000-xxxxxxx"}, rp.prune,
		"the leftover budget after tombstones goes to the oldest prunes; a zero leftover must yield zero "+
			"prunes, never the whole list")
}
