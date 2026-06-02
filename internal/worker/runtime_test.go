package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEngine struct {
	registered []WorkflowDef
	started    bool
	stopped    bool
	regErr     error
}

func (f *fakeEngine) Register(def WorkflowDef) error {
	if f.regErr != nil {
		return f.regErr
	}
	f.registered = append(f.registered, def)
	return nil
}

func (f *fakeEngine) Start(context.Context) error { f.started = true; return nil }
func (f *fakeEngine) Stop(context.Context) error  { f.stopped = true; return nil }

func noop(context.Context, map[string]any) error { return nil }

func TestWorkerBoot(t *testing.T) {
	eng := &fakeEngine{}
	rt := NewRuntime(eng)

	perSite := []string{WorkflowGCSite, WorkflowManualDelete, WorkflowSitePurge}
	for _, name := range perSite {
		require.NoError(t, rt.Register(WorkflowDef{Name: name, ConcurrencyKey: ConcurrencyKeySite, Handler: noop}))
	}
	require.NoError(t, rt.Register(WorkflowDef{Name: WorkflowTombstonePurge, Handler: noop}))

	require.NoError(t, rt.Start(context.Background()))
	assert.True(t, eng.started, "Start delegates to the engine")

	byName := map[string]WorkflowDef{}
	for _, d := range eng.registered {
		byName[d.Name] = d
	}
	require.Len(t, eng.registered, 4)
	for _, name := range perSite {
		assert.Equal(t, ConcurrencyKeySite, byName[name].ConcurrencyKey,
			"%s must register with concurrency key=site (V7)", name)
	}
	assert.Empty(t, byName[WorkflowTombstonePurge].ConcurrencyKey,
		"tombstone-purge is a cross-site sweep, not per-site keyed")

	require.NoError(t, rt.Stop(context.Background()))
	assert.True(t, eng.stopped)
}

func TestRuntime_RejectsDuplicateAndNil(t *testing.T) {
	rt := NewRuntime(&fakeEngine{})
	require.NoError(t, rt.Register(WorkflowDef{Name: "x", Handler: noop}))
	require.Error(t, rt.Register(WorkflowDef{Name: "x", Handler: noop}), "duplicate name rejected")
	require.Error(t, rt.Register(WorkflowDef{Name: "y"}), "nil handler rejected")
	require.Error(t, rt.Register(WorkflowDef{Handler: noop}), "empty name rejected")
}

func TestRuntime_PropagatesRegisterError(t *testing.T) {
	rt := NewRuntime(&fakeEngine{regErr: errors.New("boom")})
	err := rt.Register(WorkflowDef{Name: "x", Handler: noop})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register x")
}
