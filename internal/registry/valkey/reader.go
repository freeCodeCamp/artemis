package valkey

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

// onRefreshErrorFn names the OnRefreshError callback type so it can
// be stored inside atomic.Pointer (which requires a concrete type
// parameter).
type onRefreshErrorFn = func(error)

// DefaultRefreshFallback is the cap on how long the in-memory cache
// can stay stale without an explicit registry.changed event refresh.
// Even if pub-sub silently drops a message, callers see at most this
// much divergence between Valkey state and the artemis snapshot.
const DefaultRefreshFallback = 60 * time.Second

// Reader is the registry.Reader implementation backed by Valkey.
// It maintains an in-process snapshot of the entire registry that
// is refreshed eagerly on every registry.changed event and lazily
// on a TTL fallback (covers missed pub-sub deliveries).
type Reader struct {
	store *Store

	mu       sync.RWMutex
	snapshot snapshot

	// onRefreshError is invoked for every refresh that errored out of
	// run(). The previous snapshot stays served; this hook is the only
	// way an external metrics layer learns about the stale read. Set
	// via SetOnRefreshError after NewReader returns. atomic.Pointer
	// gives the test goroutine and the run() goroutine a documented
	// happens-before edge — direct field access would race under -race.
	onRefreshError atomic.Pointer[onRefreshErrorFn]
}

// SetOnRefreshError installs (or clears, with nil) the callback fired
// when a registry refresh fails. Safe to call concurrently with a
// running reader; in production cmd/artemis sets it once after
// NewReader returns and never mutates.
func (r *Reader) SetOnRefreshError(f func(error)) {
	if f == nil {
		r.onRefreshError.Store(nil)
		return
	}
	fn := onRefreshErrorFn(f)
	r.onRefreshError.Store(&fn)
}

// snapshot is the immutable cached view returned to callers. It
// implements registry.Snapshot. Callers may mutate the slices
// returned from Sites/TeamsForSite — the snapshot returns fresh
// copies on every call.
type snapshot struct {
	bySite map[string][]string
}

// Sites returns the registered slugs sorted ascending. The returned
// slice is a fresh copy; callers may mutate freely.
func (s snapshot) Sites() []string {
	out := make([]string, 0, len(s.bySite))
	for k := range s.bySite {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TeamsForSite returns the team slugs authorized for the given site,
// or nil when the slug is absent. The returned slice is a fresh copy.
func (s snapshot) TeamsForSite(slug string) []string {
	teams, ok := s.bySite[slug]
	if !ok {
		return nil
	}
	out := make([]string, len(teams))
	copy(out, teams)
	return out
}

// NewReader returns a Reader pre-populated with the current registry
// state. It launches a background goroutine that subscribes to
// registry.changed and refreshes the cache on every event. The
// goroutine exits when ctx is canceled. Pass DefaultRefreshFallback
// for ttl unless tests need a tighter window.
func NewReader(ctx context.Context, store *Store, ttl time.Duration) (*Reader, error) {
	r := &Reader{store: store}
	if err := r.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("registry: initial refresh: %w", err)
	}
	events, err := store.Subscribe(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry: subscribe: %w", err)
	}
	go r.run(ctx, events, ttl)
	return r, nil
}

// Snapshot returns a point-in-time view of the registry. The view
// is whatever the latest refresh observed; calls to Snapshot do
// NOT trigger a refresh themselves.
func (r *Reader) Snapshot() registry.Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot
}

// Refresh re-reads the registry from Valkey, replacing the cached
// snapshot atomically. Exposed as a public method so tests (and the
// import binary) can drive refreshes deterministically.
func (r *Reader) Refresh(ctx context.Context) error {
	sites, err := r.store.Sites(ctx)
	if err != nil {
		return err
	}
	bySite := make(map[string][]string, len(sites))
	for _, s := range sites {
		teams := make([]string, len(s.Teams))
		copy(teams, s.Teams)
		bySite[s.Slug] = teams
	}
	r.mu.Lock()
	r.snapshot = snapshot{bySite: bySite}
	r.mu.Unlock()
	return nil
}

// run drains pub-sub events and ticks a TTL fallback. Either source
// triggers a Refresh; refresh errors are logged and absorbed (the
// previous snapshot keeps serving until the next successful refresh).
func (r *Reader) run(ctx context.Context, events <-chan string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = DefaultRefreshFallback
	}
	ticker := time.NewTicker(ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			if err := r.Refresh(ctx); err != nil {
				slog.Warn("valkey registry refresh failed (event-driven)", "err", err)
				r.invokeOnRefreshError(err)
			}
		case <-ticker.C:
			if err := r.Refresh(ctx); err != nil {
				slog.Warn("valkey registry refresh failed (ttl fallback)", "err", err)
				r.invokeOnRefreshError(err)
			}
		}
	}
}

// invokeOnRefreshError fires the OnRefreshError callback (if set)
// inside a panic-recovering shim. A panicking callback would
// otherwise kill the run() goroutine and freeze the snapshot
// indefinitely; recovering keeps the stale-read mode intact and
// emits a structured log entry so the operator notices the broken
// callback.
func (r *Reader) invokeOnRefreshError(err error) {
	fp := r.onRefreshError.Load()
	if fp == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("valkey registry OnRefreshError callback panicked",
				"panic", p,
				"refresh_err", err,
			)
		}
	}()
	(*fp)(err)
}
