package gc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultTestBlastCap = 1000

func TestApplyBlastCap_ZeroRefusesEveryDestructiveRepair(t *testing.T) {
	t.Parallel()

	rc := &Reconciler{BlastCap: 0}
	plan := &repairPlan{tombstone: []string{"a", "b"}, prune: []string{"c"}}
	report := &DriftReport{}

	rc.applyBlastCap(context.Background(), "www", plan, report)

	assert.Empty(t, plan.tombstone,
		"an unset blast cap once meant unlimited, so a misconfigured environment reaped a whole site in "+
			"one run; zero must refuse instead")
	assert.Empty(t, plan.prune)
	require.True(t, report.Capped)
	assert.Contains(t, report.CapReason, "blast-cap 0")
}

func TestApplyBlastCap_LeavesAPlanWithinTheCapAlone(t *testing.T) {
	t.Parallel()

	rc := &Reconciler{BlastCap: 10}
	plan := &repairPlan{tombstone: []string{"a", "b"}, prune: []string{"c"}}
	report := &DriftReport{}

	rc.applyBlastCap(context.Background(), "www", plan, report)

	assert.Len(t, plan.tombstone, 2)
	assert.Len(t, plan.prune, 1)
	assert.False(t, report.Capped)
}

func TestApplyBlastCap_ZeroLeavesReindexAlone(t *testing.T) {
	t.Parallel()

	rc := &Reconciler{BlastCap: 0}
	plan := &repairPlan{reindex: []string{"a"}, tombstone: []string{"b"}}
	report := &DriftReport{}

	rc.applyBlastCap(context.Background(), "www", plan, report)

	assert.Equal(t, []string{"a"}, plan.reindex,
		"reindex re-adds a lost index row for bytes that already exist; it destroys nothing, so no ceiling "+
			"on destruction should suppress it")
}
