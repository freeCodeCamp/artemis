package observability

import (
	"sync"
	"time"
)

const transientEscalateCooldown = 24 * time.Hour

type transientRateTracker struct {
	mu        sync.Mutex
	clock     func() time.Time
	cooldown  time.Duration
	escalated map[string]time.Time
}

func newTransientRateTracker(clock func() time.Time, cooldown time.Duration) *transientRateTracker {
	return &transientRateTracker{
		clock:     clock,
		cooldown:  cooldown,
		escalated: make(map[string]time.Time),
	}
}

func (t *transientRateTracker) observe(op, class string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := op + "\x00" + class
	if last, ok := t.escalated[key]; ok && now.Sub(last) < t.cooldown {
		return false
	}
	t.escalated[key] = now
	return true
}

var backgroundTransientRate = newTransientRateTracker(time.Now, transientEscalateCooldown)
