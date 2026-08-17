package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCronShapedOps_CoverEveryNightlyDriftVerdict(t *testing.T) {
	t.Parallel()

	for _, op := range []string{
		"drift.sweep",
		"drift.selfcheck",
		"drift.unreadable",
		"drift.aliased_missing",
		"drift.reclaimable",
		"tombstone.purge",
	} {
		assert.True(t, cronShapedOps[op],
			"%s fires at most once a night, so the transient-rate tracker (threshold 3, 26h reset) would "+
				"swallow it for two nights before escalating — and an in-memory count resets on every pod "+
				"restart, so it may never escalate at all", op)
	}
}
