package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/cloudflare"
	"github.com/freeCodeCamp/artemis/internal/config"
)

func TestEdgePurger_NilUntilBothCredentialsArePresent(t *testing.T) {
	for name, ec := range map[string]config.EdgeCacheConfig{
		"neither":    {},
		"zone only":  {ZoneID: "zone123"},
		"token only": {APIToken: "test-placeholder-value"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Nil(t, edgePurger(&config.Config{EdgeCache: ec}),
				"a half-configured purger would fail every takedown; disabled and warned is the honest state")
		})
	}
}

func TestEdgePurger_BuiltWhenBothCredentialsArePresent(t *testing.T) {
	p := edgePurger(&config.Config{EdgeCache: config.EdgeCacheConfig{
		ZoneID: "zone123", APIToken: "test-placeholder-value",
	}})

	require.NotNil(t, p)
	c, ok := p.(*cloudflare.PurgeClient)
	require.True(t, ok)
	assert.Equal(t, "zone123", c.ZoneID)
}
