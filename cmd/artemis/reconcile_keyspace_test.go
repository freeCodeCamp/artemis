package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/handler"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const domainFormat = "<site>.freecode.camp/deploys/<ts>-<sha>/"

func TestStorageSiteNames_ProduceThePrefixTheWritePathUsed(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)
	layout, err := newGCLayout(domainFormat, "_trash/")
	require.NoError(t, err)

	slugs := []sitekey.Slug{"test", "hello-universe", "flag-frenzy"}
	names := storageSiteNames(slugs, tmpl)
	require.Len(t, names, len(slugs))

	for i, slug := range slugs {
		require.Equal(t, tmpl.SitePrefix(slug), layout.sitePrefix(names[i]),
			"slug %q: reconcile would list a prefix no deploy is stored under", slug)
	}
}

func TestStorageSiteNames_MatchTheOutboxSiteChangedForm(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)

	require.Equal(t,
		[]sitekey.Dirname{tmpl.SiteDirname("test")},
		storageSiteNames([]sitekey.Slug{"test"}, tmpl))
}

func TestStorageSiteNames_EmptyRegistryYieldsNoNames(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)
	require.Empty(t, storageSiteNames(nil, tmpl))
}

func TestStorageSiteNames_BareFormatIsIdentity(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	layout, err := newGCLayout("<site>/deploys/<ts>-<sha>/", "_trash/")
	require.NoError(t, err)

	names := storageSiteNames([]sitekey.Slug{"test", "www"}, tmpl)
	require.Equal(t, []sitekey.Dirname{"test", "www"}, names)
	require.Equal(t, tmpl.SitePrefix("test"), layout.sitePrefix(names[0]))
}

func TestGCLayout_AgreesWithTheWritePathOnEveryRenderedKey(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)
	layout, err := newGCLayout(domainFormat, "_trash/")
	require.NoError(t, err)

	for _, slug := range []string{"test", "hello-universe", "www"} {
		dirname := tmpl.SiteDirname(sitekey.Slug(slug))
		const id = "20260101-000000-abc1234"

		require.Equal(t, tmpl.SitePrefix(sitekey.Slug(slug)), layout.sitePrefix(dirname),
			"slug %q: the sweep lists a prefix the write path never produces", slug)
		require.Equal(t, tmpl.DeployPrefix(sitekey.Slug(slug), id), layout.deployPrefix(dirname, id),
			"slug %q: gc would move a prefix no deploy was written to, so the real bytes stay and the "+
				"tombstone dates nothing", slug)
		require.Equal(t, "_trash/"+string(dirname)+"/"+id+"/", layout.trashPrefix(dirname, id),
			"slug %q: tombstone-purge hard-deletes _trash/<dirname>/<id>/ by reconstructing it from the "+
				"tombstone row, so any other shape leaks bytes forever", slug)
	}
}
