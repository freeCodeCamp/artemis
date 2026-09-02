package quarantine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingTB struct {
	testing.TB
	skipped string
	fatal   string
	logged  string
}

func (r *recordingTB) Skipf(format string, args ...any)  { r.skipped = fmt.Sprintf(format, args...) }
func (r *recordingTB) Fatalf(format string, args ...any) { r.fatal = fmt.Sprintf(format, args...) }
func (r *recordingTB) Logf(format string, args ...any)   { r.logged = fmt.Sprintf(format, args...) }
func (r *recordingTB) Helper()                           {}

func TestSkip_SkipsByDefaultAndNamesRefAndExpiry(t *testing.T) {
	t.Setenv(RunEnv, "")
	tb := &recordingTB{TB: t}

	Skip(tb, "artemis#123", "2026-10-01")

	require.Empty(t, tb.fatal)
	assert.Contains(t, tb.skipped, "artemis#123")
	assert.Contains(t, tb.skipped, "2026-10-01")
	assert.Contains(t, tb.skipped, RunEnv)
}

func TestSkip_RunsWhenEnvSet(t *testing.T) {
	t.Setenv(RunEnv, "1")
	tb := &recordingTB{TB: t}

	Skip(tb, "artemis#123", "2026-10-01")

	assert.Empty(t, tb.skipped)
	assert.Empty(t, tb.fatal)
	assert.Contains(t, tb.logged, "artemis#123")
}

func TestSkip_MalformedDateIsFatal(t *testing.T) {
	t.Setenv(RunEnv, "")
	tb := &recordingTB{TB: t}

	Skip(tb, "artemis#123", "next month")

	assert.Contains(t, tb.fatal, "next month")
	assert.Empty(t, tb.skipped)
}

func TestSkip_ExpiredStillSkipsInsideTheBinary(t *testing.T) {
	t.Setenv(RunEnv, "")
	tb := &recordingTB{TB: t}

	Skip(tb, "artemis#123", "2020-01-01")

	assert.Empty(t, tb.fatal)
	assert.Contains(t, tb.skipped, "2020-01-01")
}
