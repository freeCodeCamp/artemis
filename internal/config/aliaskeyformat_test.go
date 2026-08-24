package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config/configtest"
)

func aliasKeyEnv() map[string]string {
	return map[string]string{
		"GH_CLIENT_ID":         "cid",
		"JWT_SIGNING_KEY":      "0123456789abcdef0123456789abcdef",
		"R2_ENDPOINT":          "http://127.0.0.1:1",
		"R2_ACCESS_KEY_ID":     "k",
		"R2_SECRET_ACCESS_KEY": "s",
		"VALKEY_ADDR":          "127.0.0.1:6379",
	}
}

func TestLoad_AcceptsTheDefaultAliasKeyFormats(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "<site>/production", c.Aliases.ProductionKeyFormat)
	require.Equal(t, "<site>/preview", c.Aliases.PreviewKeyFormat)
}

func TestLoad_RejectsAnAliasKeyFormatWithoutSiteToken(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "aliases/production")

	_, err := Load()
	require.ErrorContains(t, err, "ALIAS_PRODUCTION_KEY_FORMAT")
	require.ErrorContains(t, err, "must contain <site>",
		"the segment-equality rule also rejects this, but with a message about segments; the "+
			"likeliest misconfiguration deserves the direct answer")
}

func TestLoad_RejectsAnAliasKeyFormatKeepingSiteAfterTheSegment(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("ALIAS_PREVIEW_KEY_FORMAT", "<site>/aliases/<site>")

	_, err := Load()
	require.ErrorContains(t, err, "ALIAS_PREVIEW_KEY_FORMAT",
		"the purge path passes the rendered alias key to MovePrefix as a prefix; a trailing <site> "+
			"is fetched literally and would 404 for every site")
}

func TestLoad_RejectsAnAliasKeyFormatEndingAtTheSiteSegment(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "aliases/<site>")

	_, err := Load()
	require.Error(t, err,
		"site 'learn' would render prefix 'aliases/learn', which also matches site 'learn-beta' and "+
			"would unpublish a different site during a purge")
	require.ErrorContains(t, err, "ALIAS_PRODUCTION_KEY_FORMAT")
}

func TestLoad_RejectsAnAliasKeyFormatUnderADifferentSiteSegment(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site>.freecode.camp/deploys/<ts>-<sha>/")
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "<site>.example.test/production")
	t.Setenv("ALIAS_PREVIEW_KEY_FORMAT", "<site>.freecode.camp/preview")

	_, err := Load()
	require.ErrorContains(t, err, "ALIAS_PRODUCTION_KEY_FORMAT",
		"the alias must live under the same site segment as the deploy prefix, or the purge's "+
			"HasPrefix check looks somewhere the alias never was")
}

func TestLoad_RejectsADeployPrefixWhoseSiteSegmentIsNotRenderableFromTheSlug(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site><ts>-<sha>/")

	_, err := Load()
	require.ErrorContains(t, err, "DEPLOY_PREFIX_FORMAT")
	require.ErrorContains(t, err, "<ts>")
	require.NotContains(t, err.Error(), "ALIAS_",
		"the culprit is the deploy layout; blaming the alias format sends the operator to the wrong variable")
}

func TestLoad_RejectsADeployPrefixWithNoSiteSegment(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("DEPLOY_PREFIX_FORMAT", "deploys/<site>/<ts>-<sha>/")

	_, err := Load()
	require.ErrorContains(t, err, "DEPLOY_PREFIX_FORMAT")
	require.NotContains(t, err.Error(), "ALIAS_",
		"every storage dirname is rendered from the segment before the first slash; a constant "+
			"segment collapses every site onto one prefix, and that is the deploy layout's fault")
}

func TestLoad_RejectsADeployPrefixWithNoSlash(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site>-<ts>-<sha>")

	_, err := Load()
	require.ErrorContains(t, err, "DEPLOY_PREFIX_FORMAT")
	require.ErrorContains(t, err, "'/'",
		"the site segment is everything before the first slash; with no slash there is no dirname")
}

func TestLoad_RejectsAnAliasKeyFormatWithNoSlash(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("ALIAS_PREVIEW_KEY_FORMAT", "<site>")

	_, err := Load()
	require.ErrorContains(t, err, "ALIAS_PREVIEW_KEY_FORMAT")
	require.ErrorContains(t, err, "'/'")
}

func TestLoad_ReturnsTheAliasKeyTailsInProductionThenPreviewOrder(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())

	c, err := Load()
	require.NoError(t, err)

	tails, err := c.AliasKeyTails()
	require.NoError(t, err)
	require.Equal(t, []string{"production", "preview"}, tails,
		"cmd/artemis/main.go picks tails[0] for mode==production and tails[1] otherwise, so a swapped "+
			"order writes every production alias key at the preview tail")
}

func TestLoad_RejectsAnAliasKeyFormatWithNoTail(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), aliasKeyEnv())
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "<site>/")

	_, err := Load()
	require.ErrorContains(t, err, "ALIAS_PRODUCTION_KEY_FORMAT")
	require.ErrorContains(t, err, "names no object after its site segment",
		"an alias key that ends at the site segment renders to the bare site prefix, so the purge "+
			"unpublish stage would MovePrefix the whole site instead of one alias object")
}
