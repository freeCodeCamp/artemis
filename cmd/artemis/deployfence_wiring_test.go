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

func TestBuildHandlers_WiresTheDeployFenceInTheProductionShape(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.TTL = 12 * time.Minute

	h := buildHandlers(cfg, handlerDeps{
		registry: fencelessWriter{},
		health:   &valkey.Store{},
	})

	require.NotNil(t, h.DeployFence,
		"openRegistry returns pg.RegistryStore as the writer whenever DATABASE_URL is set, and only "+
			"*valkey.Store carries the fence, so asserting off the registry leaves production unfenced "+
			"while a test that feeds a valkey store as the registry passes")
	assert.Equal(t, 12*time.Minute, h.DeployJWTTTL,
		"the fence must outlive exactly the permit that could abuse it, no longer")
}

func TestBuildHandlers_LeavesTheDeployFenceNilWithoutAValkeyStore(t *testing.T) {
	h := buildHandlers(&config.Config{}, handlerDeps{registry: fencelessWriter{}})

	assert.Nil(t, h.DeployFence,
		"a deployment with no valkey store must leave the field untyped-nil so the unwired warn fires")
}

type fencelessWriter struct{ handler.RegistryWriter }
