package sites

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
