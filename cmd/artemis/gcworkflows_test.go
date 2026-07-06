package main

import (
	"context"
	"errors"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingPublisher struct {
	topics   []string
	payloads [][]byte
}

func (f *capturingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	f.topics = append(f.topics, topic)
	f.payloads = append(f.payloads, payload)
	return nil
}

func noSites() []string { return nil }

func TestGCWorkflowDefs(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, true, nil, &capturingPublisher{}, noSites)
	require.Len(t, defs, 4)

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
	assert.Equal(t, []string{topicSiteReconcile}, rec.EventTriggers, "reconcile consumes site.reconcile events")

	sched := byName[workflowReconcileScheduler]
	assert.NotEmpty(t, sched.Cron, "reconcile scheduler is cron-triggered — the missing producer for site.reconcile")
	assert.Empty(t, sched.EventTriggers, "scheduler is not itself event-driven")
}

func TestReconcileScheduler_PublishesPerSite(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	pub := &capturingPublisher{}
	sites := func() []string { return []string{"www", "learn"} }
	defs := gcWorkflowDefs(gcw, true, nil, pub, sites)

	var sched worker.WorkflowDef
	for _, d := range defs {
		if d.Name == workflowReconcileScheduler {
			sched = d
		}
	}
	require.NotNil(t, sched.Handler)
	require.NoError(t, sched.Handler(context.Background(), nil))

	require.Len(t, pub.topics, 2, "one site.reconcile event published per registered site")
	assert.Equal(t, []string{topicSiteReconcile, topicSiteReconcile}, pub.topics)
	assert.Contains(t, string(pub.payloads[0]), `"site":"www"`)
	assert.Contains(t, string(pub.payloads[1]), `"site":"learn"`)
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

func TestObserveWorkflow_RecordsOutcome(t *testing.T) {
	cases := []struct {
		name         string
		inner        error
		wantErr      bool
		wantRuns     float64
		wantFailures float64
		outcome      string
	}{
		{name: "failed-run-bumps-failures", inner: errors.New("boom"), wantErr: true, wantRuns: 1, wantFailures: 1, outcome: "failed"},
		{name: "ok-run-leaves-failures-zero", inner: nil, wantErr: false, wantRuns: 1, wantFailures: 0, outcome: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := worker.NewMetrics(prometheus.NewRegistry())
			wrapped := observeWorkflow(m, worker.WorkflowGCSite, func(context.Context, map[string]any) error {
				return tc.inner
			})

			err := wrapped(context.Background(), nil)
			if tc.wantErr {
				require.Error(t, err, "wrapper must propagate the inner error")
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tc.wantRuns,
				testutil.ToFloat64(m.WorkflowRuns.WithLabelValues(worker.WorkflowGCSite, tc.outcome)),
				"runs{outcome=%s} must be recorded", tc.outcome)
			assert.Equal(t, tc.wantFailures,
				testutil.ToFloat64(m.WorkflowFailures.WithLabelValues(worker.WorkflowGCSite)),
				"WorkflowFailures is the alerting signal; failed runs must bump it, ok runs must not")
		})
	}
}

func TestGCWorkflowHandlers_RejectMissingSite(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Reconciler: &gc.Reconciler{}, Purge: &gc.TombstonePurge{}}
	defs := gcWorkflowDefs(gcw, true, nil, &capturingPublisher{}, noSites)
	byName := map[string]worker.WorkflowDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}

	cases := []struct {
		name     string
		workflow string
		input    map[string]any
	}{
		{name: "gc-site-empty-input", workflow: worker.WorkflowGCSite, input: map[string]any{}},
		{name: "reconcile-empty-site", workflow: worker.WorkflowReconcile, input: map[string]any{"site": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := byName[tc.workflow]
			require.NotNil(t, def.Handler, "%s handler must be set", tc.workflow)
			err := def.Handler(context.Background(), tc.input)
			require.ErrorContains(t, err, "missing site",
				"the siteFromInput guard must short-circuit before SiteGC.Run on a nil/empty site, or a mass-move could target the wrong prefix")
		})
	}
}

type captureEngine struct{ defs []worker.WorkflowDef }

func (c *captureEngine) Register(d worker.WorkflowDef) error { c.defs = append(c.defs, d); return nil }
func (c *captureEngine) Start(context.Context) error         { return nil }
func (c *captureEngine) Stop(context.Context) error          { return nil }

func TestRegisterGCWorkflows(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	rt := worker.NewRuntime(&captureEngine{})
	require.NoError(t, registerGCWorkflows(rt, gcw, false, nil, &capturingPublisher{}, noSites))
	assert.Len(t, rt.Registered(), 4)
}
