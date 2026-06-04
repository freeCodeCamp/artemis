package worker

import (
	"sync"
	"time"
)

type Debouncer struct {
	Window  time.Duration
	Trigger func(site string)

	mu      sync.Mutex
	gen     uint64
	timers  map[string]debounceEntry
	stopped bool
}

type debounceEntry struct {
	timer *time.Timer
	gen   uint64
}

func NewDebouncer(window time.Duration, trigger func(site string)) *Debouncer {
	return &Debouncer{Window: window, Trigger: trigger, timers: map[string]debounceEntry{}}
}

func (d *Debouncer) Notify(site string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	if e, ok := d.timers[site]; ok {
		e.timer.Stop()
	}
	d.gen++
	gen := d.gen
	timer := time.AfterFunc(d.Window, func() { d.fire(site, gen) })
	d.timers[site] = debounceEntry{timer: timer, gen: gen}
}

func (d *Debouncer) fire(site string, gen uint64) {
	d.mu.Lock()
	e, ok := d.timers[site]
	if d.stopped || !ok || e.gen != gen {
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
	for _, e := range d.timers {
		e.timer.Stop()
	}
	d.timers = map[string]debounceEntry{}
}
