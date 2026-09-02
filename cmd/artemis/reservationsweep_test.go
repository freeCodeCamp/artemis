package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedReclaimables struct {
	rows   []registry.Reservation
	before time.Time
	ttl    time.Duration
	limit  int
	err    error
}

func (s *scriptedReclaimables) ReclaimableReservations(_ context.Context, before time.Time, ttl time.Duration, limit int) ([]registry.Reservation, error) {
	s.before, s.ttl, s.limit = before, ttl, limit
	if s.err != nil {
		return nil, s.err
	}
	if len(s.rows) > limit {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

type recordingEmitter struct {
	batches [][]pg.SiteLifecycleEvent
	err     error
}

func (e *recordingEmitter) EnqueueSiteLifecycle(_ context.Context, events []pg.SiteLifecycleEvent) error {
	if e.err != nil {
		return e.err
	}
	e.batches = append(e.batches, events)
	return nil
}

func testDirname(s sitekey.Slug) sitekey.Dirname {
	return sitekey.Dirname(string(s) + ".freecode.camp")
}

func fixedNow() time.Time { return time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC) }

func TestScheduleReclaims_EmitsOneEventPerReclaimableRowInOneBatch(t *testing.T) {
	src := &scriptedReclaimables{rows: []registry.Reservation{
		{Slug: "old-one", ReservedUntil: fixedNow().Add(-time.Hour)},
		{Slug: "old-two", ReservedUntil: fixedNow().Add(-time.Minute)},
	}}
	emit := &recordingEmitter{}

	n, err := scheduleReclaims(context.Background(), src, emit, testDirname, fixedNow, false)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	require.Len(t, emit.batches, 1,
		"one outbox transaction per run: a crash mid-emit leaves zero events, never a partial batch")
	assert.Equal(t, []pg.SiteLifecycleEvent{
		{Action: pg.LifecycleActionReclaim, Slug: "old-one", Site: "old-one.freecode.camp"},
		{Action: pg.LifecycleActionReclaim, Slug: "old-two", Site: "old-two.freecode.camp"},
	}, emit.batches[0])
	assert.Equal(t, fixedNow(), src.before,
		"the cutoff must be now; a future cutoff would emit names still inside their grace")
	assert.Equal(t, reclaimClaimTTL, src.ttl,
		"a row claimed inside the TTL belongs to a run that may still be moving bytes")
	assert.Equal(t, reservationSweepLimit, src.limit)
}

func TestScheduleReclaims_EmitsAtMostTheSweepLimit(t *testing.T) {
	rec := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(rec)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var rows []registry.Reservation
	for i := 0; i < reservationSweepLimit+7; i++ {
		rows = append(rows, registry.Reservation{Slug: sitekey.Slug("s" + string(rune('a'+i%26)) + string(rune('a'+i/26)))})
	}
	src := &scriptedReclaimables{rows: rows}
	emit := &recordingEmitter{}

	n, err := scheduleReclaims(context.Background(), src, emit, testDirname, fixedNow, false)

	require.NoError(t, err)
	assert.Equal(t, reservationSweepLimit, n,
		"ADR-022 keeps reservationSweepLimit as the nightly cap; the rest wait for tomorrow")
	require.Len(t, emit.batches, 1)
	assert.Len(t, emit.batches[0], reservationSweepLimit)
	level, logged := rec.levelOf("reservation.sweep.capped")
	require.True(t, logged, "a capped night must say so; silence reads as an empty backlog")
	assert.Equal(t, slog.LevelWarn, level, "warn is what reaches the alert channel; an info line on a capped night is silence")
	assert.Equal(t, strconv.Itoa(reservationSweepLimit), rec.attr("reservation.sweep.capped", "limit"), "the warning names the cap so the reader knows the rest waits for tomorrow")
}

func TestScheduleReclaims_DryRunEmitsNothing(t *testing.T) {
	src := &scriptedReclaimables{rows: []registry.Reservation{{Slug: "old-one"}}}
	emit := &recordingEmitter{}

	n, err := scheduleReclaims(context.Background(), src, emit, testDirname, fixedNow, true)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, emit.batches, "CLEANUP_DRY_RUN must reach every destructive leg, including the emit")
}

func TestScheduleReclaims_NothingToEmitTouchesNothing(t *testing.T) {
	emit := &recordingEmitter{}

	n, err := scheduleReclaims(context.Background(), &scriptedReclaimables{}, emit, testDirname, fixedNow, false)

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, emit.batches, "an empty batch must not open an outbox transaction")
}

func TestScheduleReclaims_SurfacesAnEmitFailure(t *testing.T) {
	src := &scriptedReclaimables{rows: []registry.Reservation{{Slug: "old-one"}}}
	emit := &recordingEmitter{err: errors.New("outbox down")}

	n, err := scheduleReclaims(context.Background(), src, emit, testDirname, fixedNow, false)

	require.Error(t, err, "a batch that never reached the outbox must surface, never be swallowed")
	assert.Zero(t, n)
}

func TestScheduleReclaims_NoStoreIsANoOp(t *testing.T) {
	n, err := scheduleReclaims(context.Background(), nil, nil, testDirname, fixedNow, false)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestScheduleReclaims_LiveRunWithoutADirnameIsAWiringError(t *testing.T) {
	src := &scriptedReclaimables{rows: []registry.Reservation{{Slug: "old-one"}}}

	_, err := scheduleReclaims(context.Background(), src, &recordingEmitter{}, nil, fixedNow, false)

	require.Error(t, err, "an event without a site names no prefix; the step would fail every run")
}

func TestReclaimEmitCarriesConcurrencyKey(t *testing.T) {
	gcw := &gcWiring{}
	defs := gcWorkflowDefs(gcw, true, cleanSweep)
	var found bool
	var keys []string
	for _, d := range defs {
		if d.Name != worker.WorkflowSiteLifecycle {
			continue
		}
		found = true
		keys = append(keys, d.ConcurrencyKey)
		for _, l := range d.ExtraConcurrency {
			keys = append(keys, l.Key)
		}
	}
	require.True(t, found, "the site.lifecycle workflow must be registered")

	b, err := json.Marshal(pg.SiteLifecycleEvent{Action: pg.LifecycleActionReclaim, Slug: "palette", Site: "palette.freecode.camp"})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(b, &payload))
	for _, k := range keys {
		assert.Contains(t, payload, k,
			"the engine evaluates input.<key> per strategy and FAILS the task when the key is absent; "+
				"contract row 10 alone passes while every run fails")
	}
}
