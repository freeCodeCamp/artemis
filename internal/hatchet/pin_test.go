package hatchet

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHatchetLitePinMatchesGoMod(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	modMatch := regexp.MustCompile(`github\.com/hatchet-dev/hatchet v([0-9.]+)`).FindSubmatch(gomod)
	require.NotNil(t, modMatch, "go.mod must pin github.com/hatchet-dev/hatchet")
	want := string(modMatch[1])

	var globs []string
	for _, pattern := range []string{"../../test/*/compose*.yaml", "../../test/*/*/compose*.yaml", "../../*compose*.yml", "../../*compose*.yaml"} {
		m, gerr := filepath.Glob(pattern)
		require.NoError(t, gerr)
		globs = append(globs, m...)
	}
	require.NotEmpty(t, globs, "compose files must be discoverable from this package")

	liteRe := regexp.MustCompile(`hatchet-lite:v([0-9.]+)`)
	found := 0
	for _, f := range globs {
		body, rerr := os.ReadFile(f)
		require.NoError(t, rerr)
		for _, m := range liteRe.FindAllSubmatch(body, -1) {
			found++
			require.Equal(t, want, string(m[1]),
				"%s pins hatchet-lite v%s but go.mod pins v%s; every compose engine must stay in lockstep with the client", f, m[1], want)
		}
	}
	require.NotZero(t, found, "at least one compose file must pin hatchet-lite, or this guard is vacuous")
}
