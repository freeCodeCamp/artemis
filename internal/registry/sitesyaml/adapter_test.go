package sitesyaml_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/registry/sitesyaml"
	"github.com/freeCodeCamp/artemis/internal/sites"
)

// writeSitesYAML drops a sites.yaml fixture at path and returns the
// path. The fixture is deliberately small — the adapter is a
// pass-through; deep loader semantics are exercised by sites_test.go.
func writeSitesYAML(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "sites.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestAdapter_SatisfiesReader(t *testing.T) {
	t.Parallel()

	// Compile-time interface assertion: if Adapter ever stops
	// satisfying registry.Reader this test fails to build.
	var _ registry.Reader = (*sitesyaml.Adapter)(nil)
}

func TestAdapter_SnapshotPassesThroughToLoader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := writeSitesYAML(t, dir, `sites:
  blog:
    teams: [news-editors, platform]
  certifications:
    teams: [platform]
`)
	loader, err := sites.New(p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = loader.Close() })

	a := sitesyaml.New(loader)
	snap := a.Snapshot()

	require.ElementsMatch(t, []string{"blog", "certifications"}, snap.Sites())
	require.ElementsMatch(t, []string{"news-editors", "platform"}, snap.TeamsForSite("blog"))
	require.ElementsMatch(t, []string{"platform"}, snap.TeamsForSite("certifications"))
	require.Nil(t, snap.TeamsForSite("absent"))
}
