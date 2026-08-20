package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var everyEnvReadInThisPackage = regexp.MustCompile(`(?:os\.LookupEnv|os\.Getenv|getEnv)\("([A-Z0-9_]+)"\)`)

func envKeysReadInPackageSource(t *testing.T) map[string]bool {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)

	read := map[string]bool{}
	scanned := 0
	for _, f := range sources {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		scanned++
		for _, m := range everyEnvReadInThisPackage.FindAllStringSubmatch(string(src), -1) {
			read[m[1]] = true
		}
	}
	require.NotZero(t, scanned, "globbed no package source at all")
	require.NotEmpty(t, read, "the matcher found no reads, so the package changed shape")
	return read
}

func TestEnvKeys_ListsExactlyTheVariablesThePackageReads(t *testing.T) {
	listed := map[string]bool{}
	for _, k := range EnvKeys() {
		listed[k] = true
	}

	assert.Equal(t, sortedKeys(envKeysReadInPackageSource(t)), sortedKeys(listed),
		"a stale list leaves the newest variable leaking in from the developer's shell, "+
			"which is the failure it exists to prevent; every non-test file in the package is scanned, "+
			"so moving a read to a sibling file cannot hide it")
}

func TestEnvKeys_HasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range EnvKeys() {
		assert.False(t, seen[k], "%s is listed twice", k)
		seen[k] = true
	}
}

func TestEnvKeys_ReturnsACopyCallersCannotCorrupt(t *testing.T) {
	first := EnvKeys()
	require.NotEmpty(t, first)
	first[0] = "MUTATED_BY_A_CALLER"

	assert.NotEqual(t, "MUTATED_BY_A_CALLER", EnvKeys()[0])
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
