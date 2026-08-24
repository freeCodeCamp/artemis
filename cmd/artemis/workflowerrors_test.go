package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/worker"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func defByName(t *testing.T, defs []worker.WorkflowDef, name string) worker.WorkflowDef {
	t.Helper()
	for _, d := range defs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("workflow %q not registered", name)
	return worker.WorkflowDef{}
}

func TestDriftDetectWorkflow_PropagatesASweepFailure(t *testing.T) {
	var captured string
	restore := captureBackground
	captureBackground = func(op string, _ error) { captured = op }
	t.Cleanup(func() { captureBackground = restore })

	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	failing := func(context.Context) (sweepResult, error) { return sweepResult{}, errors.New("list r2: down") }
	def := defByName(t, gcWorkflowDefs(gcw, true, failing), worker.WorkflowDriftDetect)

	err := def.Handler(context.Background(), nil)

	require.ErrorContains(t, err, "list r2",
		"a sweep that could not run must fail the workflow so the cron check-in goes red")
	assert.Equal(t, opDriftSweep, captured)
}

func TestDriftDetectWorkflow_NeedsNoInput(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	def := defByName(t, gcWorkflowDefs(gcw, true, cleanSweep), worker.WorkflowDriftDetect)

	require.NoError(t, def.Handler(context.Background(), map[string]any{}),
		"the sweep enumerates the fleet itself, so no producer has to name a site for it")
}

func TestGCSiteWorkflow_RejectsMissingSiteInput(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	def := defByName(t, defs, worker.WorkflowGCSite)

	require.Error(t, def.Handler(context.Background(), map[string]any{}))
}

type failingRuntime struct{ err error }

func (r *failingRuntime) Register(worker.WorkflowDef) error { return r.err }

func TestRegisterGCWorkflows_PropagatesRegisterError(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}

	err := registerGCWorkflows(&failingRuntime{err: errors.New("engine gone")}, gcw, true, cleanSweep)

	require.ErrorContains(t, err, "engine gone")
}

type errGCStore struct {
	gc.Store
	err error
}

func (s *errGCStore) DeploysForSite(context.Context, sitekey.Dirname) ([]gc.Deploy, error) {
	return nil, s.err
}

type errReaper struct {
	gc.TombstoneReaper
	err error
}

func (r *errReaper) ExpiredTombstones(context.Context, time.Time) ([]gc.Tombstone, error) {
	return nil, r.err
}

func TestGCSiteWorkflow_PropagatesStoreError(t *testing.T) {
	gcw := &gcWiring{
		SiteGC:     &gc.SiteGC{Store: &errGCStore{err: errors.New("pg down")}, Now: time.Now},
		Purge:      &gc.TombstonePurge{},
		Reconciler: &gc.Reconciler{},
	}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	def := defByName(t, defs, worker.WorkflowGCSite)

	require.Error(t, def.Handler(context.Background(), map[string]any{"site": "www"}))
}

func TestTombstonePurgeWorkflow_PropagatesReaperError(t *testing.T) {
	gcw := &gcWiring{
		SiteGC:     &gc.SiteGC{},
		Purge:      &gc.TombstonePurge{Store: &errReaper{err: errors.New("pg down")}, Now: time.Now},
		Reconciler: &gc.Reconciler{},
	}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	def := defByName(t, defs, worker.WorkflowTombstonePurge)

	require.Error(t, def.Handler(context.Background(), nil))
}

func TestOpenPostgres_UnreachableDSNFails(t *testing.T) {
	cfg := &config.Config{DatabaseURL: "postgres://nobody@127.0.0.1:1/none?sslmode=disable"}
	cfg.PGConnectRetryWindow = 0

	_, _, err := openPostgres(context.Background(), cfg)
	require.Error(t, err)
}

type stubAliasGetter struct {
	val string
	err error
}

func (g stubAliasGetter) GetAlias(context.Context, string) (string, error) {
	return g.val, g.err
}

func TestNewLiveAliasReader_PropagatesGetterError(t *testing.T) {
	read := newLiveAliasReader(stubAliasGetter{err: errors.New("r2 down")}, "production")

	_, err := read(context.Background(), "www")
	require.ErrorContains(t, err, "r2 down")
}

func TestNewLiveAliasReader_CollectsTargets(t *testing.T) {
	read := newLiveAliasReader(stubAliasGetter{val: "d1"}, "production", "preview")

	got, err := read(context.Background(), "www")
	require.NoError(t, err)
	require.Contains(t, got, "d1")
}

type nopReconcileStore struct{}

func (nopReconcileStore) DeploysForSite(context.Context, sitekey.Dirname) ([]gc.Deploy, error) {
	return nil, nil
}

func (nopReconcileStore) AliasTargets(context.Context, sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	return map[string]struct{}{}, time.Time{}, nil
}

func (nopReconcileStore) ReindexDeploy(context.Context, sitekey.Dirname, string, time.Time, bool) (bool, error) {
	return true, nil
}
func (nopReconcileStore) RecordTombstone(context.Context, sitekey.Dirname, string, int64) error {
	return nil
}
func (nopReconcileStore) PruneDeploy(context.Context, sitekey.Dirname, string) error { return nil }

func TestOpenTeamCache_DisabledWithoutValkeyAddr(t *testing.T) {
	cache, cleanup, err := openTeamCache(context.Background(), &config.Config{})

	require.NoError(t, err)
	require.Nil(t, cache, "no valkey addr means no shared team cache")
	require.NotNil(t, cleanup)
	cleanup()
}

func TestOpenTeamCache_UnreachableValkeyFails(t *testing.T) {
	cfg := &config.Config{}
	cfg.Registry.Valkey.Addr = "127.0.0.1:1"
	cfg.Registry.Valkey.RetryWindow = 0

	_, cleanup, err := openTeamCache(context.Background(), cfg)

	require.Error(t, err)
	require.NotNil(t, cleanup, "cleanup must stay safe to call after a failed connect")
	cleanup()
}
