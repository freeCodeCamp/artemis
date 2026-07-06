package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReaper struct{}

func (fakeReaper) ExpiredTombstones(context.Context, time.Time) ([]gc.Tombstone, error) {
	return nil, nil
}
func (fakeReaper) ClearTombstone(context.Context, string, string) error { return nil }

func TestCronCheckIn_ReconcileAndPurge(t *testing.T) {
	type ci struct {
		slug   string
		sched  string
		status sentry.CheckInStatus
	}
	var got []ci
	orig := captureCheckIn
	captureCheckIn = func(c *sentry.CheckIn, cfg *sentry.MonitorConfig) *sentry.EventID {
		b, _ := json.Marshal(cfg.Schedule)
		var s struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(b, &s)
		got = append(got, ci{c.MonitorSlug, s.Value, c.Status})
		id := sentry.EventID("stub")
		return &id
	}
	t.Cleanup(func() { captureCheckIn = orig })

	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{Store: fakeReaper{}, Now: time.Now}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, true, nil, &capturingPublisher{}, noSites)
	byName := map[string]worker.WorkflowDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}

	require.NoError(t, byName[workflowReconcileScheduler].Handler(context.Background(), nil))
	require.NoError(t, byName[worker.WorkflowTombstonePurge].Handler(context.Background(), nil))

	require.Len(t, got, 4, "two check-ins (in_progress+ok) per cron workflow")
	assert.Equal(t, ci{workflowReconcileScheduler, cronReconcile, sentry.CheckInStatusInProgress}, got[0])
	assert.Equal(t, workflowReconcileScheduler, got[1].slug)
	assert.Equal(t, sentry.CheckInStatusOK, got[1].status)
	assert.Equal(t, ci{worker.WorkflowTombstonePurge, cronTombstonePurge, sentry.CheckInStatusInProgress}, got[2])
	assert.Equal(t, worker.WorkflowTombstonePurge, got[3].slug)
	assert.Equal(t, sentry.CheckInStatusOK, got[3].status)
}

func TestWithCheckIn_ErrorStatus(t *testing.T) {
	var statuses []sentry.CheckInStatus
	orig := captureCheckIn
	captureCheckIn = func(c *sentry.CheckIn, _ *sentry.MonitorConfig) *sentry.EventID {
		statuses = append(statuses, c.Status)
		return nil
	}
	t.Cleanup(func() { captureCheckIn = orig })

	wrapped := withCheckIn("slug", "0 4 * * *", func(context.Context, map[string]any) error {
		return errors.New("boom")
	})
	require.Error(t, wrapped(context.Background(), nil))
	assert.Equal(t, []sentry.CheckInStatus{sentry.CheckInStatusInProgress, sentry.CheckInStatusError}, statuses)
}

type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) attr(msg, key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message != msg {
			continue
		}
		var out string
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out = a.Value.String()
				return false
			}
			return true
		})
		return out
	}
	return ""
}

func TestWorkflowScope_RunID(t *testing.T) {
	rec := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(rec)))
	t.Cleanup(func() { slog.SetDefault(old) })

	wrapped := observeWorkflow(nil, worker.WorkflowGCSite, func(context.Context, map[string]any) error { return nil })
	require.NoError(t, wrapped(context.Background(), nil))

	runID := rec.attr("workflow.start", "run_id")
	assert.NotEmpty(t, runID, "workflow.start line carries a run_id")
	assert.Equal(t, runID, rec.attr("workflow.done", "run_id"), "same run_id on the done line")
}

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
