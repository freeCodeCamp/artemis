package config

import (
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config/configtest"

	"github.com/stretchr/testify/require"
)

func publicURLEnv() map[string]string {
	return map[string]string{
		"GH_CLIENT_ID":         "cid",
		"JWT_SIGNING_KEY":      "0123456789abcdef0123456789abcdef",
		"R2_ENDPOINT":          "http://127.0.0.1:1",
		"R2_ACCESS_KEY_ID":     "k",
		"R2_SECRET_ACCESS_KEY": "s",
		"VALKEY_ADDR":          "127.0.0.1:6379",
	}
}

func TestLoad_PublicURLFormatsDefaultToTheServedHostShapes(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), publicURLEnv())

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://<site>.freecode.camp", c.Aliases.ProductionURLFormat)
	require.Equal(t, "https://<site>.preview.freecode.camp", c.Aliases.PreviewURLFormat)
}

func TestLoad_PublicURLFormatsAreOverridable(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), publicURLEnv())
	t.Setenv("PUBLIC_URL_PRODUCTION_FORMAT", "https://<site>.example.test")
	t.Setenv("PUBLIC_URL_PREVIEW_FORMAT", "https://<site>.pre.example.test")

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://<site>.example.test", c.Aliases.ProductionURLFormat)
	require.Equal(t, "https://<site>.pre.example.test", c.Aliases.PreviewURLFormat)
}

func TestLoad_RejectsAPublicURLFormatWithoutSiteToken(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), publicURLEnv())
	t.Setenv("PUBLIC_URL_PRODUCTION_FORMAT", "https://freecode.camp")

	_, err := Load()
	require.ErrorContains(t, err, "<site>",
		"a public URL that ignores the site would hand every deploy the same wrong link")
	require.ErrorContains(t, err, "PUBLIC_URL_PRODUCTION_FORMAT",
		"the operator has to edit one variable, so the message must name which one")
}

func TestLoad_NamesTheProductionURLFormatFirstWhenBothAreInvalid(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), publicURLEnv())
	t.Setenv("PUBLIC_URL_PRODUCTION_FORMAT", "https://freecode.camp")
	t.Setenv("PUBLIC_URL_PREVIEW_FORMAT", "https://preview.freecode.camp")

	_, err := Load()

	require.ErrorContains(t, err, "PUBLIC_URL_PRODUCTION_FORMAT")
	require.NotContains(t, err.Error(), "PUBLIC_URL_PREVIEW_FORMAT",
		"a boot failure that names a different variable on each restart sends the operator hunting; "+
			"the check must walk the same order the alias key formats already do")
}

func TestLoad_BlastCapDefaultsToARealCeiling(t *testing.T) {
	configtest.Hermetic(t, EnvKeys(), publicURLEnv())

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, 10, c.Cleanup.BlastCap,
		"an absent CLEANUP_BLAST_CAP once left the cap at 0, which both consumers read as unlimited")
}
