package main

import (
	"context"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGCWorkflowDefs(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, true, nil)
	require.Len(t, defs, 3)

	byName := map[string]worker.WorkflowDef{}
	for _, d := range defs {
		byName[d.Name] = d
		require.NotNilf(t, d.Handler, "%s handler must be set", d.Name)
	}

	gcSite := byName[worker.WorkflowGCSite]
	assert.Equal(t, worker.ConcurrencyKeySite, gcSite.ConcurrencyKey, "gc-site serialized per site (V3)")
	assert.Equal(t, []string{pg.TopicSiteChanged}, gcSite.EventTriggers, "gc-site triggered by the outbox topic")

	purge := byName[worker.WorkflowTombstonePurge]
	assert.Empty(t, purge.ConcurrencyKey, "tombstone-purge is global")
	assert.NotEmpty(t, purge.Cron, "tombstone-purge is scheduled")

	rec := byName[worker.WorkflowReconcile]
	assert.Equal(t, worker.ConcurrencyKeySite, rec.ConcurrencyKey, "reconcile serialized per site")
}

func TestSiteFromInput(t *testing.T) {
	s, err := siteFromInput(map[string]any{"site": "www.freecode.camp"})
	require.NoError(t, err)
	assert.Equal(t, "www.freecode.camp", s)

	_, err = siteFromInput(map[string]any{})
	require.Error(t, err, "missing site rejected")
	_, err = siteFromInput(map[string]any{"site": ""})
	require.Error(t, err, "empty site rejected")
}

type captureEngine struct{ defs []worker.WorkflowDef }

func (c *captureEngine) Register(d worker.WorkflowDef) error { c.defs = append(c.defs, d); return nil }
func (c *captureEngine) Start(context.Context) error         { return nil }
func (c *captureEngine) Stop(context.Context) error          { return nil }

func TestRegisterGCWorkflows(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	rt := worker.NewRuntime(&captureEngine{})
	require.NoError(t, registerGCWorkflows(rt, gcw, false, nil))
	assert.Len(t, rt.Registered(), 3)
}
