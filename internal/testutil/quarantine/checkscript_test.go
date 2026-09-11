package quarantine_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pkgPath = "github.com/freeCodeCamp/artemis/internal/testutil/" + "quarantine"
	skipFn  = "quarantine" + ".Skip"
	dotFn   = "Skip"
)

func fixture(importLine, call string) string {
	return fmt.Sprintf(`package sample

import (
	"testing"

	%s
)

func TestFlaky(t *testing.T) {
	%s
}
`, importLine, call)
}

func plainImport() string { return fmt.Sprintf("%q", pkgPath) }
func dotImport() string   { return fmt.Sprintf(". %q", pkgPath) }

func runCheckScript(t *testing.T, filename, body string) (string, int) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, filename), []byte(body), 0o600))

	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "quarantine-check.sh"))
	require.NoError(t, err)

	out, err := exec.Command("bash", script, "check", root).CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	return string(out), exitErr.ExitCode()
}

func TestCheckScript_ActiveQuarantinePasses(t *testing.T) {
	body := fixture(plainImport(), fmt.Sprintf(`%s(t, "artemis#111", "2099-01-01")`, skipFn))
	out, code := runCheckScript(t, "sample_test.go", body)

	assert.Equal(t, 0, code, out)
	assert.Contains(t, out, "active")
}

func TestCheckScript_ExpiredQuarantineFails(t *testing.T) {
	body := fixture(plainImport(), fmt.Sprintf(`%s(t, "artemis#111", "2020-01-01")`, skipFn))
	out, code := runCheckScript(t, "sample_test.go", body)

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "EXPIRED")
}

func TestCheckScript_DotImportFails(t *testing.T) {
	body := fixture(dotImport(), fmt.Sprintf(`%s(t, "artemis#111", "2099-01-01")`, dotFn))
	out, code := runCheckScript(t, "sample_test.go", body)

	assert.Equal(t, 1, code,
		"a dot-import hides the call from the registry scan, so the quarantine would never expire")
	assert.Contains(t, out, "ALIASED-IMPORT-HIDES-CALLS")
}

func TestCheckScript_NonLiteralArgsFail(t *testing.T) {
	body := fixture(plainImport(), fmt.Sprintf(`%s(t, "artemis#111", expires)`, skipFn)) +
		"\nconst expires = \"2099-01-01\"\n"
	out, code := runCheckScript(t, "sample_test.go", body)

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "NON-LITERAL-ARGS")
}

func TestCheckScript_CallOutsideTestFileFails(t *testing.T) {
	body := fixture(plainImport(), fmt.Sprintf(`%s(t, "artemis#111", "2099-01-01")`, skipFn))
	out, code := runCheckScript(t, "prod.go", body)

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "NOT-A-TEST-FILE")
}
