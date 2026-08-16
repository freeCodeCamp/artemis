package observability

import (
	"sync"
	"time"
)

const (
	defaultTransientRateThreshold = 3
	defaultTransientResetWindow   = 26 * time.Hour
	defaultTransientReescalateGap = 24 * time.Hour
)

type transientOpState struct {
	count       int
	lastSeen    time.Time
	escalatedAt time.Time
}

type transientRateTracker struct {
	mu          sync.Mutex
	clock       func() time.Time
	resetWindow time.Duration
	reescalate  time.Duration
	threshold   int
	states      map[string]*transientOpState
}

func newTransientRateTracker(clock func() time.Time, resetWindow time.Duration, threshold int) *transientRateTracker {
	return &transientRateTracker{
		clock:       clock,
		resetWindow: resetWindow,
		reescalate:  defaultTransientReescalateGap,
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
		st.escalatedAt = time.Time{}
	}
	st.count++
	st.lastSeen = now
	if st.count >= t.threshold && (st.escalatedAt.IsZero() || now.Sub(st.escalatedAt) >= t.reescalate) {
		st.escalatedAt = now
		return true
	}
	return false
}

var backgroundTransientRate = newTransientRateTracker(time.Now, defaultTransientResetWindow, defaultTransientRateThreshold)
