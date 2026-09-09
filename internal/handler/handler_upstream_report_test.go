package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUpstreamReportAllowed_BoundsOneOpToOneEventPerWindow(t *testing.T) {
	t0 := time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)
	assert.True(t, upstreamReportAllowed("r2.put.object.test-a", t0))
	for i := range 5000 {
		assert.False(t, upstreamReportAllowed("r2.put.object.test-a", t0.Add(time.Duration(i)*time.Millisecond)),
			"a 5000-file deploy against a broken r2 key must not burn 5000 Sentry events on one fingerprint")
	}
	assert.True(t, upstreamReportAllowed("r2.put.object.test-a", t0.Add(upstreamReportWindow)),
		"the next window reports again, so a lasting fault stays visible")
}

func TestUpstreamReportAllowed_DoesNotSuppressADifferentOp(t *testing.T) {
	t0 := time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)
	assert.True(t, upstreamReportAllowed("valkey.get.test-b", t0))
	assert.True(t, upstreamReportAllowed("r2.list.test-b", t0),
		"the limiter is per-op; one noisy op must never hide a second, unrelated failure")
}
