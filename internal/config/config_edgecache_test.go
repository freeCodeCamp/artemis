package config

import (
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config/configtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_EdgeCacheDefaultsOff(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), requiredEnv())

	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.EdgeCache.Enabled(), "no purge without a zone and a token")
}

func TestLoad_EdgeCacheParsed(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), requiredEnv())
	t.Setenv("CF_ZONE_ID", "zone123")
	t.Setenv("CF_PURGE_API_TOKEN", "tok")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "zone123", cfg.EdgeCache.ZoneID)
	assert.Equal(t, "tok", cfg.EdgeCache.APIToken)
	assert.True(t, cfg.EdgeCache.Enabled())
}

func TestLoad_EdgeCachePartialConfigFails(t *testing.T) {
	for _, key := range []string{"CF_ZONE_ID", "CF_PURGE_API_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			configtest.Hermetic(t, EnvKeys(), requiredEnv())
			t.Setenv(key, "x")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "partial")
		})
	}
}

func TestLoad_EdgeCacheBlankZoneIsPartial(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), requiredEnv())
	t.Setenv("CF_ZONE_ID", "  ")
	t.Setenv("CF_PURGE_API_TOKEN", "tok")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partial")
}
