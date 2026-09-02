package quarantine

import (
	"os"
	"testing"
	"time"
)

const RunEnv = "ARTEMIS_RUN_QUARANTINED"

const dateLayout = "2006-01-02"

func Skip(t testing.TB, ref, expires string) {
	t.Helper()
	if _, err := time.Parse(dateLayout, expires); err != nil {
		t.Fatalf("quarantine %s: expiry %q is not YYYY-MM-DD", ref, expires)
		return
	}
	if os.Getenv(RunEnv) != "" {
		t.Logf("quarantined %s until %s: running because %s is set", ref, expires, RunEnv)
		return
	}
	t.Skipf("quarantined %s until %s (set %s to run it)", ref, expires, RunEnv)
}
