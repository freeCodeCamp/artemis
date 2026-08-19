package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

// TestDeployPrefixTemplate_RendersDefault — sanity for the canonical
// production format. Anchors B7 against regression.
func TestDeployPrefixTemplate_RendersDefault(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/")
	require.NoError(t, err)

	assert.Equal(t, "www/deploys/", tpl.SitePrefix("www"))
	assert.Equal(t, "www/deploys/20260420-141522-abc1234/",
		tpl.DeployPrefix("www", "20260420-141522-abc1234"))
}

// TestDeployPrefixTemplate_NonDefaultFormat — operator-chosen format.
// Pre-B7 stripDeployIDFromFmt mis-parsed any non-default token shape and
// silently produced a wrong R2 listing prefix. Parser must split cleanly
// regardless of intermediate path segments.
func TestDeployPrefixTemplate_NonDefaultFormat(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>/d/<ts>-<sha>/sub/")
	require.NoError(t, err)

	assert.Equal(t, "www/d/", tpl.SitePrefix("www"))
	assert.Equal(t, "www/d/20260420-141522-abc1234/sub/",
		tpl.DeployPrefix("www", "20260420-141522-abc1234"))
}

// TestDeployPrefixTemplate_AppendsTrailingSlash — both renderers must
// guarantee a trailing slash so callers can concatenate relative file
// paths without double-checking.
func TestDeployPrefixTemplate_AppendsTrailingSlash(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>")
	require.NoError(t, err)

	assert.True(t, hasSuffix(tpl.SitePrefix("www"), "/"))
	assert.True(t, hasSuffix(tpl.DeployPrefix("www", "id"), "/"))
}

// TestDeployPrefixTemplate_RejectsMalformed — parser refuses inputs
// that lack the required tokens. Validate() at config load is the
// primary gate; this is the in-handler last line of defence in case
// Handlers is built directly in tests / future callers.
func TestDeployPrefixTemplate_RejectsMalformed(t *testing.T) {
	cases := []string{
		"hello",
		"<ts>-<sha>/no-site/",
		"<site>/no-id-token/",
		"<ts>-<sha>/<site>/wrong-order/", // <site> must appear before <ts>-<sha>
	}
	for _, c := range cases {
		_, err := NewDeployPrefixTemplate(c)
		require.Error(t, err, "expected error for %q", c)
	}
}

func hasSuffix(s, suf string) bool {
	if len(s) < len(suf) {
		return false
	}
	return s[len(s)-len(suf):] == suf
}

func TestDeployPrefixTemplate_SiteSlugInvertsSiteDirname(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)

	for _, slug := range []string{"www", "test", "a", "learn-beta", "a.freecode.camp"} {
		dirname := tpl.SiteDirname(slug)
		got, ok := tpl.SiteSlug(dirname)

		require.True(t, ok, "SiteDirname(%q) rendered %q, which SiteSlug must accept", slug, dirname)
		assert.Equal(t, slug, got,
			"SiteSlug is the only sanctioned dirname->slug conversion; a slug that already ends in the "+
				"root domain must lose exactly one suffix, not two")
	}
}

func TestDeployPrefixTemplate_SiteSlugIsIdentityWhenTheFormatAddsNoAffixes(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>/deploys/<ts>-<sha>/")
	require.NoError(t, err)

	got, ok := tpl.SiteSlug("www")
	require.True(t, ok)
	assert.Equal(t, "www", got,
		"under the test format slug and dirname coincide; the inverse must not invent a difference")
}

func TestDeployPrefixTemplate_SiteSlugRejectsAForeignDirname(t *testing.T) {
	tpl, err := NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)

	for _, dirname := range []sitekey.Dirname{"", "_trash", "www.example.com", ".freecode.camp"} {
		_, ok := tpl.SiteSlug(dirname)
		assert.False(t, ok,
			"%q does not render from any slug under this format; returning a plausible-looking slug "+
				"would write a third keyspace into audit_log", dirname)
	}
}
