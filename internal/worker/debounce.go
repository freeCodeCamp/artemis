package worker

import (
	"sync"
	"time"
)

type Debouncer struct {
	Window  time.Duration
	Trigger func(site string)

	mu      sync.Mutex
	timers  map[string]*time.Timer
	stopped bool
}

func NewDebouncer(window time.Duration, trigger func(site string)) *Debouncer {
	return &Debouncer{Window: window, Trigger: trigger, timers: map[string]*time.Timer{}}
}

func (d *Debouncer) Notify(site string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if t, ok := d.timers[site]; ok {
		t.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(d.Window, func() { d.fire(site, timer) })
	d.timers[site] = timer
}

func (d *Debouncer) fire(site string, timer *time.Timer) {
	d.mu.Lock()
	if d.stopped || d.timers[site] != timer {
		d.mu.Unlock()
		return
	}
	delete(d.timers, site)
	d.mu.Unlock()
	d.Trigger(site)
}

func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	for _, t := range d.timers {
		t.Stop()
	}
	d.timers = map[string]*time.Timer{}
}
