package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readsEnv(name string) bool {
	return name == "LookupEnv" || name == "Getenv" || strings.HasPrefix(name, "getEnv")
}

func calleeName(fn ast.Expr) string {
	switch f := fn.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func envKeysReadInPackageSource(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	read := map[string]bool{}
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		files++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !readsEnv(calleeName(call.Fun)) {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v, err := strconv.Unquote(lit.Value); err == nil && v != "" {
					read[v] = true
				}
			}
			return true
		})
	}
	require.NotZero(t, files, "parsed no package source at all")
	require.NotEmpty(t, read, "found no environment reads, so the package changed shape")
	return read
}

func TestEnvKeys_ListsExactlyTheVariablesThePackageReads(t *testing.T) {
	listed := map[string]bool{}
	for _, k := range EnvKeys() {
		listed[k] = true
	}

	assert.Equal(t, sortedKeys(envKeysReadInPackageSource(t)), sortedKeys(listed),
		"a stale list leaves the newest variable leaking in from the developer shell, "+
			"which is the failure it exists to prevent; every non-test file in the package is parsed "+
			"and every string argument to an env-reading call is collected, so neither a sibling file "+
			"nor a multi-argument helper can hide a key")
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
