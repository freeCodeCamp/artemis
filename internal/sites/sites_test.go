package sites

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

const validYAML = `
sites:
  www:
    teams:
      - team-eng
      - team-platform
  learn:
    teams:
      - team-eng
`

const updatedYAML = `
sites:
  www:
    teams:
      - team-eng
  learn:
    teams:
      - team-eng
  news:
    teams:
      - team-content
`

const invalidYAML = `
sites:
  www:
    teams: not-a-list
`

func TestLoad_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sites.yaml")
	writeFile(t, p, validYAML)

	l, err := New(p)
	require.NoError(t, err)
	defer l.Close()

	snap := l.Snapshot()
	assert.ElementsMatch(t, []string{"team-eng", "team-platform"}, snap.TeamsForSite("www"))
	assert.ElementsMatch(t, []string{"team-eng"}, snap.TeamsForSite("learn"))
	assert.Nil(t, snap.TeamsForSite("does-not-exist"))
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "absent.yaml"))
	require.Error(t, err)
}

func TestLoad_InvalidSchemaInitialFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sites.yaml")
	writeFile(t, p, invalidYAML)

	_, err := New(p)
	require.Error(t, err)
}

func TestHotReload_PicksUpChanges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sites.yaml")
	writeFile(t, p, validYAML)

	l, err := New(p)
	require.NoError(t, err)
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, l.Watch(ctx))

	require.Eventually(t, func() bool {
		return len(l.Snapshot().TeamsForSite("www")) == 2
	}, 2*time.Second, 20*time.Millisecond)

	// Atomic-ish replace: write to temp + rename so fsnotify sees a single Create/Rename event.
	tmp := p + ".tmp"
	writeFile(t, tmp, updatedYAML)
	require.NoError(t, os.Rename(tmp, p))

	require.Eventually(t, func() bool {
		s := l.Snapshot()
		return len(s.TeamsForSite("news")) == 1 && len(s.TeamsForSite("www")) == 1
	}, 3*time.Second, 50*time.Millisecond, "expected reload to surface news + shrunk www teams")
}

func TestHotReload_RetainsLastGoodOnInvalid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sites.yaml")
	writeFile(t, p, validYAML)

	l, err := New(p)
	require.NoError(t, err)
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, l.Watch(ctx))

	// Write garbage in place.
	tmp := p + ".tmp"
	writeFile(t, tmp, invalidYAML)
	require.NoError(t, os.Rename(tmp, p))

	// Wait long enough for fsnotify+debounce window to lapse.
	time.Sleep(300 * time.Millisecond)

	// Last-good config remains in place.
	snap := l.Snapshot()
	assert.ElementsMatch(t, []string{"team-eng", "team-platform"}, snap.TeamsForSite("www"))

	// Errors counter incremented.
	assert.GreaterOrEqual(t, l.ReloadErrors(), uint64(1))
}

// TestEventMatches_KubernetesConfigMapPattern — B19 unit gate.
// Validates the event-filter logic directly so the production behavior
// (Linux/inotify reliably emits CREATE on `..data` after a ConfigMap
// rotation) is verified without depending on darwin/kqueue quirks
// during local test runs.
func TestEventMatches_KubernetesConfigMapPattern(t *testing.T) {
	const path = "/etc/artemis/sites.yaml"
	l := &Loader{path: path}

	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"path direct write", fsnotify.Event{Name: path, Op: fsnotify.Write}, true},
		{"path direct create", fsnotify.Event{Name: path, Op: fsnotify.Create}, true},
		{"sibling ..data create (k8s rotate)",
			fsnotify.Event{Name: "/etc/artemis/..data", Op: fsnotify.Create}, true},
		{"sibling ..data remove (k8s rotate intermediate)",
			fsnotify.Event{Name: "/etc/artemis/..data", Op: fsnotify.Remove}, true},
		{"sibling ..2024_v1 dir create (irrelevant)",
			fsnotify.Event{Name: "/etc/artemis/..2024_v1", Op: fsnotify.Create}, false},
		{"unrelated sibling (irrelevant)",
			fsnotify.Event{Name: "/etc/artemis/notes.md", Op: fsnotify.Create}, false},
		{"different dir (irrelevant)",
			fsnotify.Event{Name: "/var/foo/..data", Op: fsnotify.Create}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, l.eventMatches(tc.ev))
		})
	}
}

// TestHotReload_KubernetesConfigMapAtomicRename — B19: Kubernetes
// ConfigMap projection mounts use a symlink farm:
//
//	mount/
//	  ..data        -> ..2024_xx        (symlink, atomically swapped)
//	  sites.yaml    -> ..data/sites.yaml
//	  ..2024_xx/sites.yaml (real file)
//
// On config update, k8s atomically renames `..data` to point at a new
// timestamped directory. fsnotify on the parent directory sees
// CREATE/REMOVE events on the literal `..data` entry — NOT on
// sites.yaml itself (the symlink chain is fs-transparent). Pre-B19
// the loader filtered events strictly by ev.Name == path and missed
// every ConfigMap rotation.
//
// Linux-only: darwin/kqueue does not reliably surface dir-level
// events for symlink-rename atoms (the inode-based watcher misses
// the parent-dir NOTE_WRITE for some symlink-target swaps). The
// production target is Linux/inotify which handles this case. The
// unit-level filter logic is verified above by
// TestEventMatches_KubernetesConfigMapPattern.
func TestHotReload_KubernetesConfigMapAtomicRename(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("darwin/kqueue does not reliably surface dir-level events on symlink-rename; unit-level eventMatches test covers correctness")
	}

	dir := t.TempDir()
	v1 := filepath.Join(dir, "..2024_v1")
	v2 := filepath.Join(dir, "..2024_v2")
	require.NoError(t, os.MkdirAll(v1, 0o700))
	require.NoError(t, os.MkdirAll(v2, 0o700))
	writeFile(t, filepath.Join(v1, "sites.yaml"), validYAML)
	writeFile(t, filepath.Join(v2, "sites.yaml"), updatedYAML)

	dataLink := filepath.Join(dir, "..data")
	require.NoError(t, os.Symlink(v1, dataLink))

	mounted := filepath.Join(dir, "sites.yaml")
	require.NoError(t, os.Symlink(filepath.Join("..data", "sites.yaml"), mounted))

	l, err := New(mounted)
	require.NoError(t, err)
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, l.Watch(ctx))

	// Sanity: initial snapshot is v1.
	require.Eventually(t, func() bool {
		return len(l.Snapshot().TeamsForSite("www")) == 2
	}, 2*time.Second, 20*time.Millisecond)

	// Atomically swap `..data` → v2. Mirrors k8s ConfigMap projection.
	tmpLink := filepath.Join(dir, "..data.tmp")
	require.NoError(t, os.Symlink(v2, tmpLink))
	require.NoError(t, os.Rename(tmpLink, dataLink))

	require.Eventually(t, func() bool {
		s := l.Snapshot()
		return len(s.TeamsForSite("news")) == 1 && len(s.TeamsForSite("www")) == 1
	}, 3*time.Second, 50*time.Millisecond,
		"loader must reload on ..data symlink swap (k8s ConfigMap atomic rename)")
}

func TestSnapshot_IsImmutable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sites.yaml")
	writeFile(t, p, validYAML)

	l, err := New(p)
	require.NoError(t, err)
	defer l.Close()

	snap := l.Snapshot()
	teams := snap.TeamsForSite("www")
	teams[0] = "mutated"

	// Mutating the slice returned by Snapshot must not affect the loader's view.
	assert.NotContains(t, l.Snapshot().TeamsForSite("www"), "mutated")
}
