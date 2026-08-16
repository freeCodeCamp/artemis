package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/handler"
)

const domainFormat = "<site>.freecode.camp/deploys/<ts>-<sha>/"

func TestStorageSiteNames_ProduceThePrefixTheWritePathUsed(t *testing.T) {
	t.Parallel()

	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)
	layout, err := newGCLayout(domainFormat, "_trash/")
	require.NoError(t, err)

	slugs := []string{"test", "hello-universe", "flag-frenzy"}
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
		[]string{tmpl.SiteDirname("test")},
		storageSiteNames([]string{"test"}, tmpl))
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

	names := storageSiteNames([]string{"test", "www"}, tmpl)
	require.Equal(t, []string{"test", "www"}, names)
	require.Equal(t, tmpl.SitePrefix("test"), layout.sitePrefix(names[0]))
}
