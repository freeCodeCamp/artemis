package observability

import (
	"testing"
	"time"
)

func TestTransientRateTracker_FirstObserveEscalates(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 24*time.Hour)

	if !tr.observe("op", classPGInRecovery, base) {
		t.Fatal("1st observe must escalate: a per-process streak cannot measure a fleet-wide rate")
	}
	if tr.observe("op", classPGInRecovery, base) {
		t.Fatal("an immediate repeat must stay latched")
	}
}

func TestTransientRateTracker_ReescalatesAfterCooldown(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 24*time.Hour)

	if !tr.observe("op", classPGInRecovery, base) {
		t.Fatal("1st observe must escalate")
	}
	if tr.observe("op", classPGInRecovery, base.Add(23*time.Hour)) {
		t.Fatal("re-escalation inside the cooldown must stay latched")
	}
	if !tr.observe("op", classPGInRecovery, base.Add(24*time.Hour)) {
		t.Fatal("the boundary is >= cooldown, which is why a cron-shaped op short-circuits the tracker")
	}
}

func TestTransientRateTracker_DistinctCausesIndependent(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 24*time.Hour)

	if !tr.observe("relay.run", classPGInRecovery, base) {
		t.Fatal("1st observe must escalate")
	}
	if !tr.observe("relay.run", classPGConnClosed, base) {
		t.Fatal("a second cause on the same op keeps its own cooldown")
	}
	if !tr.observe("gc.site.run", classPGInRecovery, base) {
		t.Fatal("the same cause on a second op keeps its own cooldown")
	}
	if tr.observe("relay.run", classPGInRecovery, base) {
		t.Fatal("the first cause stays latched")
	}
}
