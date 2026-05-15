package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
