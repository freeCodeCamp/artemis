// Package sites loads the site→teams authorization map from a YAML file
// and hot-reloads it on disk changes via fsnotify. On reload errors the
// last-good config is retained and an error counter is incremented for
// observability.
package sites

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Snapshot is an immutable, copy-on-read view of the site→teams map.
type Snapshot struct {
	bySite map[string][]string
}

// NewSnapshot builds a snapshot directly from a site→teams map. Used by
// tests to avoid spinning up a real fsnotify watcher. The map is copied
// defensively so callers can't mutate the snapshot post-hoc.
func NewSnapshot(bySite map[string][]string) Snapshot {
	cp := make(map[string][]string, len(bySite))
	for k, v := range bySite {
		dup := make([]string, len(v))
		copy(dup, v)
		cp[k] = dup
	}
	return Snapshot{bySite: cp}
}

// Sites returns the sorted list of registered site keys.
func (s Snapshot) Sites() []string {
	out := make([]string, 0, len(s.bySite))
	for k := range s.bySite {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TeamsForSite returns a copy of the authorized teams for the given site,
// or nil if the site is not registered. Mutating the returned slice has
// no effect on the loader's underlying state.
func (s Snapshot) TeamsForSite(site string) []string {
	if s.bySite == nil {
		return nil
	}
	teams, ok := s.bySite[site]
	if !ok {
		return nil
	}
	out := make([]string, len(teams))
	copy(out, teams)
	return out
}

// Sites is the on-disk layout of sites.yaml.
type schema struct {
	Sites map[string]struct {
		Teams []string `yaml:"teams"`
	} `yaml:"sites"`
}

// Loader watches sites.yaml and exposes the latest valid snapshot.
type Loader struct {
	path     string
	watcher  *fsnotify.Watcher
	mu       sync.RWMutex
	current  Snapshot
	errCount atomic.Uint64
	closed   atomic.Bool
}

// New reads the file at path and returns a Loader. Returns an error if
// the file cannot be read or fails YAML schema validation.
func New(path string) (*Loader, error) {
	l := &Loader{path: path}
	snap, err := readAndParse(path)
	if err != nil {
		return nil, err
	}
	l.current = snap
	return l, nil
}

// Watch starts a goroutine that hot-reloads the loader on file changes.
// The goroutine exits when ctx is canceled or Close is called.
func (l *Loader) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify watcher: %w", err)
	}
	l.watcher = w

	// Watch the parent directory so that atomic rename-based replacements
	// (write-to-temp + rename) surface as Create events on path.
	dir := filepath.Dir(l.path)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return fmt.Errorf("fsnotify add %q: %w", dir, err)
	}

	go l.run(ctx)
	return nil
}

func (l *Loader) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = l.watcher.Close()
			return
		case ev, ok := <-l.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(ev.Name) != filepath.Clean(l.path) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			l.tryReload()
		case err, ok := <-l.watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("sites watcher error", "err", err)
		}
	}
}

func (l *Loader) tryReload() {
	snap, err := readAndParse(l.path)
	if err != nil {
		l.errCount.Add(1)
		slog.Warn("sites reload failed; retaining last-good config", "path", l.path, "err", err)
		return
	}
	l.mu.Lock()
	l.current = snap
	l.mu.Unlock()
	slog.Info("sites reloaded", "path", l.path, "count", len(snap.bySite))
}

// Snapshot returns the latest valid view of the site→teams map.
func (l *Loader) Snapshot() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.current
}

// ReloadErrors returns the cumulative count of reload failures since startup.
func (l *Loader) ReloadErrors() uint64 {
	return l.errCount.Load()
}

// Close stops the watcher goroutine. Safe to call multiple times.
func (l *Loader) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	if l.watcher != nil {
		return l.watcher.Close()
	}
	return nil
}

func readAndParse(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read sites.yaml: %w", err)
	}
	var s schema
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("parse sites.yaml: %w", err)
	}
	by := make(map[string][]string, len(s.Sites))
	for site, entry := range s.Sites {
		if site == "" {
			return Snapshot{}, fmt.Errorf("sites.yaml: empty site key")
		}
		// Defensive copy.
		teams := make([]string, len(entry.Teams))
		copy(teams, entry.Teams)
		by[site] = teams
	}
	return Snapshot{bySite: by}, nil
}
