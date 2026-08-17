package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_PublicURLFormatsDefaultToTheServedHostShapes(t *testing.T) {
	t.Setenv("GH_CLIENT_ID", "cid")
	t.Setenv("JWT_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("R2_ACCESS_KEY_ID", "k")
	t.Setenv("R2_SECRET_ACCESS_KEY", "s")
	t.Setenv("VALKEY_ADDR", "127.0.0.1:6379")

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://<site>.freecode.camp", c.Aliases.ProductionURLFormat)
	require.Equal(t, "https://<site>.preview.freecode.camp", c.Aliases.PreviewURLFormat)
}

func TestLoad_PublicURLFormatsAreOverridable(t *testing.T) {
	t.Setenv("GH_CLIENT_ID", "cid")
	t.Setenv("JWT_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("R2_ACCESS_KEY_ID", "k")
	t.Setenv("R2_SECRET_ACCESS_KEY", "s")
	t.Setenv("VALKEY_ADDR", "127.0.0.1:6379")
	t.Setenv("PUBLIC_URL_PRODUCTION_FORMAT", "https://<site>.example.test")
	t.Setenv("PUBLIC_URL_PREVIEW_FORMAT", "https://<site>.pre.example.test")

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://<site>.example.test", c.Aliases.ProductionURLFormat)
	require.Equal(t, "https://<site>.pre.example.test", c.Aliases.PreviewURLFormat)
}

func TestLoad_RejectsAPublicURLFormatWithoutSiteToken(t *testing.T) {
	t.Setenv("GH_CLIENT_ID", "cid")
	t.Setenv("JWT_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("R2_ACCESS_KEY_ID", "k")
	t.Setenv("R2_SECRET_ACCESS_KEY", "s")
	t.Setenv("VALKEY_ADDR", "127.0.0.1:6379")
	t.Setenv("PUBLIC_URL_PRODUCTION_FORMAT", "https://freecode.camp")

	_, err := Load()
	require.ErrorContains(t, err, "<site>",
		"a public URL that ignores the site would hand every deploy the same wrong link")
}

func TestLoad_BlastCapDefaultsToARealCeiling(t *testing.T) {
	t.Setenv("GH_CLIENT_ID", "cid")
	t.Setenv("JWT_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("R2_ACCESS_KEY_ID", "k")
	t.Setenv("R2_SECRET_ACCESS_KEY", "s")
	t.Setenv("VALKEY_ADDR", "127.0.0.1:6379")

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, 10, c.Cleanup.BlastCap,
		"an absent CLEANUP_BLAST_CAP once left the cap at 0, which both consumers read as unlimited")
}
