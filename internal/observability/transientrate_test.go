package observability

import (
	"testing"
	"time"
)

func TestTransientRateTracker_EdgeTriggeredEscalation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 26*time.Hour, 3)

	if tr.observe("op", base) {
		t.Fatal("1st observe must not escalate")
	}
	if tr.observe("op", base.Add(time.Hour)) {
		t.Fatal("2nd observe must not escalate")
	}
	if !tr.observe("op", base.Add(2*time.Hour)) {
		t.Fatal("3rd observe must escalate")
	}
	if tr.observe("op", base.Add(3*time.Hour)) {
		t.Fatal("4th observe must not re-escalate: edge-triggered")
	}
}

func TestTransientRateTracker_LowCadenceStillEscalates(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 26*time.Hour, 3)

	if tr.observe("reconcile.schedule", base) {
		t.Fatal("1st observe must not escalate")
	}
	if tr.observe("reconcile.schedule", base.Add(24*time.Hour)) {
		t.Fatal("2nd observe (24h later) must not escalate")
	}
	if !tr.observe("reconcile.schedule", base.Add(48*time.Hour)) {
		t.Fatal("3rd observe spaced 24h apart must still escalate despite the fixed-window bug")
	}
}

func TestTransientRateTracker_GapResetsStreak(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 26*time.Hour, 3)

	if tr.observe("op", base) {
		t.Fatal("1st observe must not escalate")
	}
	if tr.observe("op", base.Add(time.Hour)) {
		t.Fatal("2nd observe must not escalate")
	}
	afterGap := base.Add(time.Hour).Add(27 * time.Hour)
	if tr.observe("op", afterGap) {
		t.Fatal("streak reset by gap: 3rd raw observe must not escalate")
	}
	if tr.observe("op", afterGap.Add(time.Hour)) {
		t.Fatal("2nd observe of the new streak must not escalate")
	}
	if !tr.observe("op", afterGap.Add(2*time.Hour)) {
		t.Fatal("3rd observe of the new streak must escalate")
	}
}

func TestTransientRateTracker_DistinctOpsIndependent(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tr := newTransientRateTracker(func() time.Time { return base }, 26*time.Hour, 3)

	tr.observe("a", base)
	tr.observe("a", base)
	if tr.observe("b", base) {
		t.Fatal("a fresh op must start its own count")
	}
}
