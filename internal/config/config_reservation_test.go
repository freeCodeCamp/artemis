package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config/configtest"
)

func TestConfig_SiteReservationGraceDefaultsTo72hAndRejectsZero(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), requiredEnv())

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 72*time.Hour, cfg.Registry.ReservationGrace)

	t.Setenv("SITE_RESERVATION_GRACE", "0s")
	_, err = Load()
	require.Error(t, err, "a zero grace would reclaim a takedown before anyone could reverse it")
	assert.Contains(t, err.Error(), "SITE_RESERVATION_GRACE")
}

func TestConfig_SiteReservationGraceIsIndependentOfCleanupGrace(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), requiredEnv())
	t.Setenv("CLEANUP_GRACE", "1h")
	t.Setenv("SITE_RESERVATION_GRACE", "168h")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, time.Hour, cfg.Cleanup.Grace)
	assert.Equal(t, 168*time.Hour, cfg.Registry.ReservationGrace,
		"the name clock and the deploy-GC clock are different lifecycles")
}

func TestConfig_SiteReservationGraceIsAKnownEnvKey(t *testing.T) {
	assert.Contains(t, EnvKeys(), "SITE_RESERVATION_GRACE",
		"a key missing from EnvKeys leaks between hermetic tests and is unswept by the env audit")
}
