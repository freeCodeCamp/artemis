package worker

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDebounce(t *testing.T) {
	var mu sync.Mutex
	fired := map[string]int{}
	d := NewDebouncer(30*time.Millisecond, func(site string) {
		mu.Lock()
		fired[site]++
		mu.Unlock()
	})
	t.Cleanup(d.Stop)

	for i := 0; i < 5; i++ {
		d.Notify("www")
	}
	d.Notify("learn")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fired["www"] == 1 && fired["learn"] == 1
	}, time.Second, 5*time.Millisecond, "a burst per site coalesces into exactly one trigger")

	mu.Lock()
	assert.Equal(t, 1, fired["www"], "5 rapid site.changed events -> 1 gc-site trigger")
	mu.Unlock()
}

func TestDebounce_LaterChangeRetriggers(t *testing.T) {
	var mu sync.Mutex
	var count int
	d := NewDebouncer(20*time.Millisecond, func(string) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	t.Cleanup(d.Stop)

	d.Notify("www")
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 1 }, time.Second, 5*time.Millisecond)

	d.Notify("www")
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 2 }, time.Second, 5*time.Millisecond,
		"a change after processing triggers GC again (no lost updates; per-site order preserved by engine key, E2)")
}

func TestDebounce_StopHaltsPendingTriggers(t *testing.T) {
	var mu sync.Mutex
	var count int
	d := NewDebouncer(50*time.Millisecond, func(string) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	d.Notify("www")
	d.Stop()
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	assert.Equal(t, 0, count, "Stop cancels pending triggers")
	mu.Unlock()
}
