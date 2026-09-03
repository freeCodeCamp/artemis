package config_test

import (
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/config/configtest"
	"github.com/stretchr/testify/require"
)

func setRequiredForSentry(t *testing.T) {
	t.Helper()
	configtest.Hermetic(t, config.EnvKeys(), map[string]string{
		"R2_ENDPOINT":          "https://acct.r2.cloudflarestorage.com",
		"R2_ACCESS_KEY_ID":     "ak",
		"R2_SECRET_ACCESS_KEY": "sk",
		"GH_CLIENT_ID":         "cid",
		"JWT_SIGNING_KEY":      "0123456789abcdef0123456789abcdef",
		"VALKEY_ADDR":          "localhost:6379",
	})
}

func TestLoad_SentryDefaultsOff(t *testing.T) {
	setRequiredForSentry(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Empty(t, cfg.Sentry.DSN, "empty DSN means the SDK stays disabled")
	require.Empty(t, cfg.Sentry.Environment)
	require.InDelta(t, 0.2, cfg.Sentry.TracesSampleRate, 1e-9)
	require.False(t, cfg.Sentry.Debug)
}

func TestLoad_SentryParsed(t *testing.T) {
	setRequiredForSentry(t)
	t.Setenv("SENTRY_DSN", "https://pub@sentry.example/42")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("SENTRY_TRACES_SAMPLE_RATE", "0.5")
	t.Setenv("SENTRY_DEBUG", "true")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "https://pub@sentry.example/42", cfg.Sentry.DSN)
	require.Equal(t, "production", cfg.Sentry.Environment)
	require.InDelta(t, 0.5, cfg.Sentry.TracesSampleRate, 1e-9)
	require.True(t, cfg.Sentry.Debug)
}

func TestLoad_SentryRateRejectsNonNumeric(t *testing.T) {
	setRequiredForSentry(t)
	t.Setenv("SENTRY_TRACES_SAMPLE_RATE", "abc")

	_, err := config.Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "SENTRY_TRACES_SAMPLE_RATE")
}

func TestLoad_SentryRateRejectsOutOfRange(t *testing.T) {
	for _, v := range []string{"-0.1", "1.5", "NaN"} {
		t.Run(v, func(t *testing.T) {
			setRequiredForSentry(t)
			t.Setenv("SENTRY_TRACES_SAMPLE_RATE", v)

			_, err := config.Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), "SENTRY_TRACES_SAMPLE_RATE")
		})
	}
}
