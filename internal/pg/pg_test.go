package pg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolConfigPinsConnectTimeout(t *testing.T) {
	t.Parallel()

	cfg, err := poolConfig("postgres://u:p@h:5432/d?sslmode=disable")

	require.NoError(t, err)
	require.Equal(t, ConnectTimeout, cfg.ConnConfig.ConnectTimeout,
		"pgxpool forces 2 minutes when the DSN leaves it unset, which outlasts the whole retry window")
}

func TestPoolConfigKeepsDSNConnectTimeout(t *testing.T) {
	t.Parallel()

	cfg, err := poolConfig("postgres://u:p@h:5432/d?sslmode=disable&connect_timeout=20")

	require.NoError(t, err)
	require.Equal(t, 20*time.Second, cfg.ConnConfig.ConnectTimeout,
		"the constant is a floor an operator raises through DATABASE_URL, never a ceiling the code imposes")
}

func TestPoolConfigRejectsMalformedDSN(t *testing.T) {
	t.Parallel()

	cfg, err := poolConfig("://not a dsn")

	require.Nil(t, cfg)
	require.ErrorContains(t, err, "pg: parse dsn")
}
