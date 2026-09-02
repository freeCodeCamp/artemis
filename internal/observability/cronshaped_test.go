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
		"drift.ledger",
	} {
		assert.True(t, cronShapedOps[op],
			"%s fires once a night, so its 24h gap sits exactly on the 24h escalation cooldown: ordinary "+
				"cron jitter (03:00:11 one night, 03:00:00 the next) would suppress a night. The "+
				"short-circuit removes the boundary", op)
	}
}

func TestCronShapedOps_CoverTheEventTriggeredReclaim(t *testing.T) {
	t.Parallel()
	assert.True(t, cronShapedOps["site.reclaim"],
		"up to 50 event-triggered reclaim runs a night share one op; the bypass keeps every failure visible instead "+
			"of collapsing the batch into one rate-limited event")
}
