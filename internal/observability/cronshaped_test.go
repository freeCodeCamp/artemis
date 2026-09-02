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
		"drift.orphan_aliases",
		"tombstone.purge",
		"reservation.sweep",
		"site.reclaim",
	} {
		assert.True(t, cronShapedOps[op],
			"%s fires once a night, so its 24h gap sits exactly on the 24h escalation cooldown: ordinary "+
				"cron jitter (03:00:11 one night, 03:00:00 the next) would suppress a night. The "+
				"short-circuit removes the boundary", op)
	}
}
