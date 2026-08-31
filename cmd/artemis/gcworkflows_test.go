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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakeReaper struct{}

func (fakeReaper) ExpiredTombstones(context.Context, time.Time) ([]gc.Tombstone, error) {
	return nil, nil
}
func (fakeReaper) TombstonesForSite(context.Context, sitekey.Dirname) ([]gc.Tombstone, error) {
	return nil, nil
}

func (fakeReaper) ClearTombstone(context.Context, sitekey.Dirname, string) (bool, error) {
	return true, nil
}

func TestCronCheckIn_DriftDetectAndPurge(t *testing.T) {
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
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	byName := map[string]worker.WorkflowDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}

	require.NoError(t, byName[worker.WorkflowDriftDetect].Handler(context.Background(), nil))
	require.NoError(t, byName[worker.WorkflowTombstonePurge].Handler(context.Background(), nil))

	require.Len(t, got, 4, "two check-ins (in_progress+ok) per cron workflow")
	assert.Equal(t, ci{worker.WorkflowDriftDetect, cronDriftDetect, sentry.CheckInStatusInProgress}, got[0])
	assert.Equal(t, worker.WorkflowDriftDetect, got[1].slug)
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

func (h *capturingHandler) levelOf(msg string) (slog.Level, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message == msg {
			return r.Level, true
		}
	}
	return 0, false
}

func cleanSweep(context.Context) (sweepResult, error) { return healthySweep(), nil }

func TestAlertOnDrift_LogsAliasedMissingAtErrorLevelNamingTheSite(t *testing.T) {
	rec := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(rec)))
	t.Cleanup(func() { slog.SetDefault(old) })
	restore := captureBackground
	captureBackground = func(string, error) {}
	t.Cleanup(func() { captureBackground = restore })

	res := healthySweep()
	res.Reports[0].Aliased = []string{"d1", "d2"}
	require.NoError(t, alertOnDrift(context.Background(), res))

	lvl, ok := rec.levelOf("drift.detected")
	require.True(t, ok, "the dangerous aliased_missing drift must surface in the log, not only in Sentry")
	assert.Equal(t, slog.LevelError, lvl, "aliased_missing is error-level: a human must investigate")
	assert.Contains(t, rec.attr("drift.detected", "err"), "www.freecode.camp",
		"the log line must name the site, or the operator cannot act on the page")
}

func TestAlertOnDrift_LogsACleanSweepBelowErrorLevel(t *testing.T) {
	rec := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(rec)))
	t.Cleanup(func() { slog.SetDefault(old) })

	require.NoError(t, alertOnDrift(context.Background(), healthySweep()))

	_, ok := rec.levelOf("drift.detected")
	assert.False(t, ok, "a sweep that finds nothing must not page anyone")
	lvl, ok := rec.levelOf("drift.clean")
	require.True(t, ok, "the clean night still has to be visible, or a silent cron reads as a healthy one")
	assert.Equal(t, slog.LevelInfo, lvl)
}

func TestWorkflowScope_RunID(t *testing.T) {
	rec := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(rec)))
	t.Cleanup(func() { slog.SetDefault(old) })

	wrapped := observeWorkflow(worker.WorkflowGCSite, func(context.Context, map[string]any) error { return nil })
	require.NoError(t, wrapped(context.Background(), nil))

	runID := rec.attr("workflow.start", "run_id")
	assert.NotEmpty(t, runID, "workflow.start line carries a run_id")
	assert.Equal(t, runID, rec.attr("workflow.done", "run_id"), "same run_id on the done line")
}

func TestGCWorkflowDefs(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	require.Len(t, defs, 3, "the per-site reconcile fan-out and its scheduler are gone")

	byName := map[string]worker.WorkflowDef{}
	for _, d := range defs {
		byName[d.Name] = d
		require.NotNilf(t, d.Handler, "%s handler must be set", d.Name)
	}

	gcSite := byName[worker.WorkflowGCSite]
	assert.Equal(t, worker.ConcurrencyKeySite, gcSite.ConcurrencyKey, "gc-site serialized per site (V3)")
	assert.Equal(t, []string{pg.TopicSiteChanged}, gcSite.EventTriggers, "gc-site triggered by the outbox topic")
	assert.GreaterOrEqual(t, gcSite.ExecutionTimeout, 10*time.Minute,
		"probed live: gc-site's Step.timeout is EMPTY while drift-detect carries 1800s, so gc-site runs "+
			"on the engine default; a blast-cap-sized run moves bytes object-by-object and a mid-run kill "+
			"strands tombstoned deploys at their prefixes until the next site.changed event")

	purge := byName[worker.WorkflowTombstonePurge]
	assert.Empty(t, purge.ConcurrencyKey, "tombstone-purge is global")
	assert.NotEmpty(t, purge.Cron, "tombstone-purge is scheduled")
	assert.GreaterOrEqual(t, purge.ExecutionTimeout, 10*time.Minute,
		"same gap as gc-site: the only hard-deleting job had no explicit budget either")

	drift := byName[worker.WorkflowDriftDetect]
	assert.NotEmpty(t, drift.Cron, "drift-detect is cron-triggered")
	assert.GreaterOrEqual(t, drift.ExecutionTimeout, 10*time.Minute,
		"the sweep lists every object of every site — 22745 objects across 76 sites in production, "+
			"minutes of work. On the engine default it would be killed every night, which is the "+
			"nightly-failing cron this change exists to stop")
	assert.Empty(t, drift.EventTriggers, "drift-detect needs no producer: it enumerates the fleet itself")
	assert.Empty(t, drift.ConcurrencyKey,
		"a read-only sweep takes no site lock, so it needs no per-site concurrency key")
}

func TestGCWorkflowDefs_NoWorkflowCanRepairOnASchedule(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}

	for _, d := range gcWorkflowDefs(gcw, true, cleanSweep) {
		assert.NotEqual(t, "reconcile", d.Name,
			"reconcile repairs bytes and is human-invoked only; a schedule must never reach it")
	}
}

func TestSiteFromInput(t *testing.T) {
	s, err := siteFromInput(map[string]any{"site": "www.freecode.camp"})
	require.NoError(t, err)
	assert.Equal(t, sitekey.Dirname("www.freecode.camp"), s)

	_, err = siteFromInput(map[string]any{})
	require.Error(t, err, "missing site rejected")
	_, err = siteFromInput(map[string]any{"site": ""})
	require.Error(t, err, "empty site rejected")
}

func TestObserveWorkflow_PropagatesError(t *testing.T) {
	cases := []struct {
		name    string
		inner   error
		wantErr bool
	}{
		{name: "failed-run-propagates", inner: errors.New("boom"), wantErr: true},
		{name: "ok-run-returns-nil", inner: nil, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := observeWorkflow(worker.WorkflowGCSite, func(context.Context, map[string]any) error {
				return tc.inner
			})

			err := wrapped(context.Background(), nil)
			if tc.wantErr {
				require.Error(t, err, "wrapper must propagate the inner error")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGCWorkflowHandlers_RejectMissingSite(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Reconciler: &gc.Reconciler{}, Purge: &gc.TombstonePurge{}}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
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
		{name: "gc-site-empty-site", workflow: worker.WorkflowGCSite, input: map[string]any{"site": ""}},
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

type lockTimeoutStore struct{}

func (lockTimeoutStore) DeploysForSite(context.Context, sitekey.Dirname) ([]gc.Deploy, error) {
	return nil, &pgconn.PgError{Code: "55P03"}
}

func (lockTimeoutStore) AliasTargets(context.Context, sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	return nil, time.Time{}, nil
}

func (lockTimeoutStore) Tombstone(context.Context, sitekey.Dirname, gc.Deploy) error { return nil }

type gcRecordingTransport struct{ events []*sentry.Event }

func (t *gcRecordingTransport) Configure(sentry.ClientOptions)        {}
func (t *gcRecordingTransport) SendEvent(e *sentry.Event)             { t.events = append(t.events, e) }
func (t *gcRecordingTransport) Flush(time.Duration) bool              { return true }
func (t *gcRecordingTransport) FlushWithContext(context.Context) bool { return true }
func (t *gcRecordingTransport) Close()                                {}

func TestGCSiteWorkflow_LockTimeoutPagesOnceInItsOwnBucket(t *testing.T) {
	rt := &gcRecordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.test/1",
		Transport: rt,
	})
	require.NoError(t, err)
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(client)
	t.Cleanup(func() { hub.BindClient(prev) })

	gcw := &gcWiring{SiteGC: &gc.SiteGC{Store: lockTimeoutStore{}}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	defs := gcWorkflowDefs(gcw, false, cleanSweep)
	var gcSite worker.WorkflowDef
	for _, d := range defs {
		if d.Name == worker.WorkflowGCSite {
			gcSite = d
		}
	}
	require.NotNil(t, gcSite.Handler)

	runErr := gcSite.Handler(context.Background(), map[string]any{"site": "www"})
	require.Error(t, runErr, "SiteGC.Run failure still propagates to the workflow engine for retry/backoff")

	sentry.CurrentHub().Flush(time.Second)
	require.Len(t, rt.events, 1,
		"lock contention is a low-severity fault, not self-inflicted cancellation: it pages once per pod per cause per 24h, in its own bucket")
	assert.Equal(t, "true", rt.events[0].Tags["transient"])
	assert.Equal(t, "pg.lock_timeout", rt.events[0].Tags["error_class"])
	assert.Equal(t, []string{"gc.site.run", "transient", "pg.lock_timeout"}, rt.events[0].Fingerprint)
}

type captureEngine struct{ defs []worker.WorkflowDef }

func (c *captureEngine) Register(d worker.WorkflowDef) error { c.defs = append(c.defs, d); return nil }
func (c *captureEngine) Start(context.Context) error         { return nil }
func (c *captureEngine) Stop(context.Context) error          { return nil }

func TestRegisterGCWorkflows(t *testing.T) {
	gcw := &gcWiring{SiteGC: &gc.SiteGC{}, Purge: &gc.TombstonePurge{}, Reconciler: &gc.Reconciler{}}
	rt := worker.NewRuntime(&captureEngine{})
	require.NoError(t, registerGCWorkflows(rt, gcw, false, cleanSweep))
	assert.Len(t, rt.Registered(), 3)
}
