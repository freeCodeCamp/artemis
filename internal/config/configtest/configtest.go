package configtest

import (
	"os"
	"slices"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/stretchr/testify/require"
)

func UnreadableKeys(want map[string]string) []string {
	known := config.EnvKeys()
	var bad []string
	for k := range want {
		if !slices.Contains(known, k) {
			bad = append(bad, k)
		}
	}
	slices.Sort(bad)
	return bad
}

func Hermetic(t *testing.T, want map[string]string) {
	t.Helper()
	require.Empty(t, UnreadableKeys(want),
		"config.Load never reads these, so setting them asserts nothing")
	for _, k := range config.EnvKeys() {
		if _, present := os.LookupEnv(k); !present {
			continue
		}
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
	for k, v := range want {
		t.Setenv(k, v)
	}
}
