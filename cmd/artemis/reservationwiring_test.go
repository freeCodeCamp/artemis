package main

import (
	"context"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production registry writer must satisfy both reservation
// interfaces, or buildHandlers silently falls back to the legacy delete
// and ADR 0006 never runs in the binary.
var (
	_ handler.ReservationStore    = (*pg.RegistryStore)(nil)
	_ handler.ReservationReverser = (*pg.RegistryStore)(nil)
)

type legacyOnlyRegistry struct{ handler.RegistryWriter }

// reservingRegistry stands in for *pg.RegistryStore: a writer that also
// reserves. The compile-time assertions above pin that the real one
// qualifies; this one lets the test hold a non-nil value.
type reservingRegistry struct{ handler.RegistryWriter }

func (reservingRegistry) Reserve(context.Context, sitekey.Slug, sitekey.Dirname, time.Time, string) (registry.Reservation, error) {
	return registry.Reservation{}, nil
}

func TestBuildHandlers_WiresTheReservationStoreWhenTheWriterSupportsIt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Registry.ReservationGrace = 72 * time.Hour
	cfg.Aliases.ProductionKeyFormat = "<site>/production"
	cfg.Aliases.PreviewKeyFormat = "<site>/preview"

	h := buildHandlers(cfg, handlerDeps{registry: reservingRegistry{}})

	require.NotNil(t, h.Reservations,
		"a nil Reservations sends every delete down the legacy path: registry row gone, r2 aliases "+
			"live, site still serving — the exact orphan defect ADR 0006 exists to fix")
	assert.Equal(t, 72*time.Hour, h.ReservationGrace,
		"a zero grace would reserve a name that expires the instant it is set")
}

func TestBuildHandlers_LeavesReservationsNilForAWriterWithoutIt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Registry.ReservationGrace = 72 * time.Hour

	h := buildHandlers(cfg, handlerDeps{registry: legacyOnlyRegistry{}})

	assert.Nil(t, h.Reservations,
		"a valkey-only deployment has no reservation table; it must keep the legacy delete rather than panic")
}

func TestWiring_NoBootConfigurationReachesTheLegacyPurge(t *testing.T) {
	cfg := &config.Config{}
	cfg.Registry.ReservationGrace = 72 * time.Hour
	cfg.Aliases.ProductionKeyFormat = "<site>/production"
	cfg.Aliases.PreviewKeyFormat = "<site>/preview"

	cases := []struct {
		name     string
		registry handler.RegistryWriter
		repo     *pg.Repo
	}{
		{"postgres backed", reservingRegistry{}, &pg.Repo{}},
		{"valkey only", legacyOnlyRegistry{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := buildHandlers(cfg, handlerDeps{registry: tc.registry})
			wirePGRepo(h, tc.repo)

			assert.Equal(t, h.Reservations != nil, h.Tombstones != nil,
				"SiteDelete's ?purge=true block runs only with Tombstones set and Reservations nil; "+
					"both arrive from the same DATABASE_URL, so that pair cannot exist")
		})
	}
}
