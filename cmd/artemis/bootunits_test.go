package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
		"info":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"DEBUG":   slog.LevelInfo,
		"verbose": slog.LevelInfo,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, parseLogLevel(in))
		})
	}
}

func TestNewGCLayout_RejectsFormatWithoutDeployIDToken(t *testing.T) {
	t.Parallel()

	_, err := newGCLayout("<site>/deploys/", "_trash/")
	require.ErrorContains(t, err, "must contain")
}

func TestNewGCLayout_RejectsFormatWithoutSlashAfterSite(t *testing.T) {
	t.Parallel()

	_, err := newGCLayout("<site><ts>-<sha>/", "_trash/")
	require.ErrorContains(t, err, "must contain '/'")
}

func TestNewGCLayout_DefaultsTrashBase(t *testing.T) {
	t.Parallel()

	layout, err := newGCLayout("<site>/deploys/<ts>-<sha>/", "")
	require.NoError(t, err)
	require.Equal(t, "_trash/www/d1/", layout.trashPrefix("www", "d1"))
}

func TestNewGCLayout_CustomTrashBase(t *testing.T) {
	t.Parallel()

	layout, err := newGCLayout("<site>/deploys/<ts>-<sha>/", "graveyard/")
	require.NoError(t, err)
	require.Equal(t, "graveyard/www/d1/", layout.trashPrefix("www", "d1"))
}

func TestGCLayout_DeployPrefixAlwaysEndsWithSlash(t *testing.T) {
	t.Parallel()

	layout, err := newGCLayout("<site>/deploys/<ts>-<sha>", "_trash/")
	require.NoError(t, err)
	require.Equal(t, "www/deploys/d1/", layout.deployPrefix("www", "d1"))
}
