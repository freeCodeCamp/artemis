package observability

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsDestructive_RawPathShapes(t *testing.T) {
	destructive := []string{
		"POST /api/site/www/promote",
		"POST /api/site/learn/rollback",
		"DELETE /api/site/www",
		"DELETE /api/site/www/deploys/20260420-141522-abc1234",
		"POST /api/site/www/deploys/20260420-141522-abc1234/restore",
		"POST /api/deploy/20260420-141522-abc1234/finalize",
		"DELETE /api/repo/42",
	}
	for _, n := range destructive {
		assert.True(t, isDestructive(n), n)
	}

	benign := []string{
		"GET /api/sites",
		"GET /api/site/www/deploys",
		"POST /api/deploy/init",
		"GET /healthz",
		"POST /api/site/www/promote/extra",
		"GET /api/site/www",
		"",
	}
	for _, n := range benign {
		assert.False(t, isDestructive(n), n)
	}
}

func TestSampleRate_ForceSamplesDestructiveNotProbe(t *testing.T) {
	assert.Equal(t, 1.0, sampleRate("POST /api/site/www/promote", 0.2), "destructive force-sampled")
	assert.Equal(t, 0.0, sampleRate("GET /healthz", 0.2), "probe dropped")
	assert.Equal(t, 0.2, sampleRate("GET /api/sites", 0.2), "normal at base rate")
}
