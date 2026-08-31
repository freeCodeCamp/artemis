package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

func TestBuildHandlers_WiresTheDeployFenceWithTheJWTTTL(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.TTL = 12 * time.Minute

	h := buildHandlers(cfg, handlerDeps{registry: &valkey.Store{}})

	require.NotNil(t, h.DeployFence,
		"unwired, a finalized deploy stays writable for the life of its permit and nothing refuses the "+
			"overwrite; the valkey store is the only shared state the three replicas agree on")
	assert.Equal(t, 12*time.Minute, h.DeployJWTTTL,
		"the fence must outlive exactly the permit that could abuse it, no longer")
}

func TestBuildHandlers_LeavesTheDeployFenceNilWithoutAValkeyStore(t *testing.T) {
	h := buildHandlers(&config.Config{}, handlerDeps{registry: stubRegistryWriter{}})

	assert.Nil(t, h.DeployFence,
		"a registry that cannot hold the fence must leave the field untyped-nil so the unwired warn fires")
}

type stubRegistryWriter struct{ handler.RegistryWriter }
