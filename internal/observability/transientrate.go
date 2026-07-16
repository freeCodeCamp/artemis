package observability

import (
	"sync"
	"time"
)

const (
	defaultTransientRateThreshold = 3
	defaultTransientResetWindow   = 26 * time.Hour
)

type transientOpState struct {
	count     int
	lastSeen  time.Time
	escalated bool
}

type transientRateTracker struct {
	mu          sync.Mutex
	clock       func() time.Time
	resetWindow time.Duration
	threshold   int
	states      map[string]*transientOpState
}

func newTransientRateTracker(clock func() time.Time, resetWindow time.Duration, threshold int) *transientRateTracker {
	return &transientRateTracker{
		clock:       clock,
		resetWindow: resetWindow,
		threshold:   threshold,
		states:      make(map[string]*transientOpState),
	}
}

func (t *transientRateTracker) observe(op string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[op]
	if !ok {
		st = &transientOpState{}
		t.states[op] = st
	}
	if !st.lastSeen.IsZero() && now.Sub(st.lastSeen) > t.resetWindow {
		st.count = 0
		st.escalated = false
	}
	st.count++
	st.lastSeen = now
	if st.count >= t.threshold && !st.escalated {
		st.escalated = true
		return true
	}
	return false
}

var backgroundTransientRate = newTransientRateTracker(time.Now, defaultTransientResetWindow, defaultTransientRateThreshold)
