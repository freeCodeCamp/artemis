package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
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

// reservingWriter stands in for *pg.RegistryStore on the GC side: a
// registry writer that also expires and releases reservations.
type reservingWriter struct{ registry.Writer }

func (reservingWriter) ExpiredReservations(context.Context, time.Time, int) ([]registry.Reservation, error) {
	return nil, nil
}

func (reservingWriter) ReleaseReservation(context.Context, sitekey.Slug) error { return nil }

func gcWiringTestConfig() *config.Config {
	return &config.Config{
		DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/",
		Aliases: config.AliasConfig{
			ProductionKeyFormat: "<site>/production",
			PreviewKeyFormat:    "<site>/preview",
		},
		Cleanup: config.CleanupConfig{BlastCap: 5, RetentionDays: 7, RecoveryDays: 3, TrashPrefix: "_trash/"},
	}
}

func TestNewGCWiring_WiresTheReservationSweepWhenTheWriterSupportsIt(t *testing.T) {
	w, err := newGCWiring(gcWiringTestConfig(), &pg.Repo{}, &r2.Client{}, reservingWriter{})
	require.NoError(t, err)
	require.NotNil(t, w)

	require.NotNil(t, w.Reservations,
		"a nil source makes sweepExpiredReservations return (0, nil) with no log and no Sentry event, "+
			"so every reserved name is held forever and its bytes are never reclaimed")
	require.NotNil(t, w.NameReleaser,
		"a nil releaser has the same silent effect as a nil source")
}

func TestNewGCWiring_LeavesTheSweepUnwiredForAWriterWithoutReservations(t *testing.T) {
	w, err := newGCWiring(gcWiringTestConfig(), &pg.Repo{}, &r2.Client{}, legacyOnlyWriter{})
	require.NoError(t, err)

	assert.Nil(t, w.Reservations,
		"a valkey-only deployment has no reservation table; the sweep must stay unwired rather than panic")
	assert.Nil(t, w.NameReleaser)
}

// legacyOnlyWriter is a registry writer with no reservation support,
// which is what a valkey-only deployment supplies.
type legacyOnlyWriter struct{ registry.Writer }

// TestNewGCWiring_AssignsEveryDependency walks the wiring struct instead
// of naming its fields one by one. A per-field assertion cannot notice a
// field the constructor forgot, which is how Reservations and
// NameReleaser stayed unset while seventeen wiring tests passed.
func TestNewGCWiring_AssignsEveryDependency(t *testing.T) {
	w, err := newGCWiring(gcWiringTestConfig(), &pg.Repo{}, &r2.Client{}, reservingWriter{})
	require.NoError(t, err)

	assertNoNilFields(t, reflect.ValueOf(*w), "gcWiring")
	assertNoNilFields(t, reflect.ValueOf(w.Reclaim), "gcWiring.Reclaim")
}

func assertNoNilFields(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	for i := range v.NumField() {
		f := v.Field(i)
		name := path + "." + v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.Interface, reflect.Ptr, reflect.Func, reflect.Map, reflect.Slice:
			assert.False(t, f.IsNil(), "%s is nil after newGCWiring; every dependency the constructor "+
				"owns must be assigned there, or a boot-order change silently disables it", name)
		}
	}
}
