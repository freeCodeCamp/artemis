package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHealth lets each readyz test drive RegistryHealth.Ping behaviour.
type fakeHealth struct {
	err error
}

func (f *fakeHealth) Ping(_ context.Context) error { return f.err }

func TestReadyZ_NoAuthRequired_BothUpstreamsReachable_ReturnsOK(t *testing.T) {
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ready":true}`, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestReadyzDegraded_PGDown_Returns200Degraded(t *testing.T) {
	h := &Handlers{
		Health:   &fakeHealth{},
		R2:       newFakeR2(),
		PGHealth: &fakeHealth{err: errors.New("dial tcp artemis-postgresql:5432: i/o timeout")},
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code, "PG down is degraded, not down — serve plane unaffected (R6/R7)")
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String())
}

func TestReadyzDegraded_PGUp_ReturnsReady(t *testing.T) {
	h := &Handlers{
		Health:   &fakeHealth{},
		R2:       newFakeR2(),
		PGHealth: &fakeHealth{},
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ready":true}`, w.Body.String())
}

func TestReadyzDegraded_ValkeyDown_Returns200Degraded(t *testing.T) {
	h := &Handlers{
		Health:   &fakeHealth{err: errors.New("valkey down")},
		R2:       newFakeR2(),
		PGHealth: &fakeHealth{},
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code, "Postgres is the registry source of truth; Valkey down is degraded, not down")
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String())
}

func TestReadyZ_ValkeyDown_PodStaysInServiceEndpoints(t *testing.T) {
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("dial tcp valkey:6379: i/o timeout")},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code,
		"all replicas share one Valkey, so a 503 here empties the Service on a single correlated fault")
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String())
}

func TestReadyZ_R2Down_Returns200Degraded_PodStaysReady(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("s3: bucket not found")
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code,
		"all replicas share one bucket, so a 503 here empties the Service on a single correlated R2 fault")
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String())
}

func TestReadyZ_R2AndPGDown_Returns200Degraded(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("s3: bucket not found")
	h := &Handlers{
		Health:   &fakeHealth{},
		R2:       r2,
		PGHealth: &fakeHealth{err: errors.New("dial tcp artemis-postgresql:5432: i/o timeout")},
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String(),
		"two degradable upstreams down still produce exactly one body carrying one degraded marker")
}

func TestReadyZ_R2Degraded_LogsAtWarn(t *testing.T) {
	cap := captureAccessLog(t)
	r2 := newFakeR2()
	r2.listErr = errors.New("s3: bucket not found")
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	w := httptest.NewRecorder()
	h.ReadyZ(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, slog.LevelWarn, cap.levelOf(t, "readyz.r2.degraded"),
		"the 200-tolerated degraded path must log at Warn, not out-rank the failing check")
}

func TestReadyZ_ValkeyAndR2Down_Returns200Degraded(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("r2 also down")
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("valkey down")},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ready":true,"degraded":true}`, w.Body.String(),
		"no upstream fails readyz closed; two down still produce one degraded marker")
}

func TestReadyz_LevelMatchesStatus(t *testing.T) {
	cap := captureAccessLog(t)

	valkey := &Handlers{Health: &fakeHealth{err: errors.New("valkey down")}, R2: newFakeR2()}
	w := httptest.NewRecorder()
	valkey.ReadyZ(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, slog.LevelWarn, cap.levelOf(t, "readyz.valkey.degraded"),
		"the 200-tolerated degraded path must log at Warn, not out-rank the failing check")

	degraded := &Handlers{Health: &fakeHealth{}, R2: newFakeR2(), PGHealth: &fakeHealth{err: errors.New("pg down")}}
	w2 := httptest.NewRecorder()
	degraded.ReadyZ(w2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, slog.LevelWarn, cap.levelOf(t, "readyz.postgres.degraded"),
		"the 200-tolerated degraded path must log at Warn, not out-rank the failing check")
}

func readyzProbe(t *testing.T, h *Handlers, hub *sentry.Hub) *httptest.ResponseRecorder {
	t.Helper()
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)
	return w
}

func TestReadyz_SingleFailure_DoesNotPage(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("dial tcp valkey:6379: connection refused")},
		R2:     newFakeR2(),
	}

	w := readyzProbe(t, h, hub)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, ft.events, "a single readyz failure is a blip (streak < threshold) — must not page")
}

func TestReadyZ_ValkeySustainedFailure_PagesAtThreshold(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	h := &Handlers{
		Health: &fakeHealth{err: fmt.Errorf("valkey ping: %w", context.DeadlineExceeded)},
		R2:     newFakeR2(),
	}

	for i := 0; i < readyzPageThreshold+5; i++ {
		require.Equal(t, http.StatusOK, readyzProbe(t, h, hub).Code,
			"paging and readiness are independent: the pod stays in the Service for the whole outage")
	}
	hub.Flush(time.Second)

	require.Len(t, ft.events, 1,
		"a sustained Valkey outage must page exactly once — the 200 is quiet, Sentry is not")
	assert.Equal(t, "valkey.ping", ft.events[0].Tags["op"])
	assert.Equal(t, []string{"readyz", "valkey.ping"}, ft.events[0].Fingerprint)
}

func TestReadyZ_R2SustainedFailure_PagesAtThreshold(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	r2.listErr = fmt.Errorf("r2 ping: %w", context.DeadlineExceeded) // wedged/black-holed upstream
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	for i := 0; i < readyzPageThreshold+5; i++ {
		require.Equal(t, http.StatusOK, readyzProbe(t, h, hub).Code,
			"paging and readiness are independent: the pod stays in the Service for the whole outage")
	}
	hub.Flush(time.Second)

	require.Len(t, ft.events, 1,
		"a SUSTAINED hang must page EXACTLY ONCE at the threshold crossing (edge-triggered) — not on every probe for the whole outage")
	assert.Equal(t, "r2.ping", ft.events[0].Tags["op"])
	assert.Equal(t, []string{"readyz", "r2.ping"}, ft.events[0].Fingerprint)
}

func TestReadyz_SuccessResetsStreak(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	probe := func(fail bool) {
		if fail {
			r2.listErr = errors.New("s3: connection refused")
		} else {
			r2.listErr = nil
		}
		readyzProbe(t, h, hub)
	}
	probe(true)
	probe(true)  // streak 2
	probe(false) // heals → reset
	probe(true)
	probe(true) // streak 2 again, never reaches 3
	hub.Flush(time.Second)

	require.Empty(t, ft.events, "a success between failures resets the streak; 2+reset+2 never crosses threshold")
}

func TestReadyz_RepagesAfterRecovery(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	drive := func(fail bool, n int) {
		for i := 0; i < n; i++ {
			if fail {
				r2.listErr = errors.New("s3: connection refused")
			} else {
				r2.listErr = nil
			}
			readyzProbe(t, h, hub)
		}
	}
	drive(true, readyzPageThreshold) // outage 1 → page once
	drive(false, 1)                  // recovery re-arms the latch
	drive(true, readyzPageThreshold) // outage 2 → page again
	hub.Flush(time.Second)

	require.Len(t, ft.events, 2, "each distinct outage episode pages once; recovery re-arms the latch")
}

func TestReadyz_DualOutage_EachUpstreamPagesOnce(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	health := &fakeHealth{}
	h := &Handlers{Health: health, R2: r2}

	health.err = errors.New("valkey down")
	r2.listErr = errors.New("r2 down")
	for i := 0; i < readyzPageThreshold+2; i++ {
		readyzProbe(t, h, hub)
	}
	health.err = nil
	for i := 0; i < 10; i++ {
		readyzProbe(t, h, hub)
	}
	hub.Flush(time.Second)

	ops := map[string]int{}
	for _, e := range ft.events {
		ops[e.Tags["op"]]++
	}
	require.Equal(t, 1, ops["r2.ping"], "the ongoing R2 outage pages once")
	require.Equal(t, 1, ops["valkey.ping"], "the healed Valkey outage pages once; neither upstream masks the other")
}

func TestProbeState_ConcurrentObserveAtThreshold_PagesExactlyOnce(t *testing.T) {
	var p probeState
	for i := 0; i < readyzPageThreshold-1; i++ {
		p.observe(true, true)
	}

	const n = 64
	var pages atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.observe(true, true) {
				pages.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), pages.Load(),
		"merged observe-and-decide latches under one lock: N concurrent threshold-crossing failures emit exactly one page")
}

func TestReadyz_ClientAbort_DoesNotCountTowardPage(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	r2.listErr = fmt.Errorf("r2 has_prefix: %w", context.Canceled)
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	for i := 0; i < readyzPageThreshold+1; i++ {
		ctx, cancel := context.WithCancel(sentry.SetHubOnContext(t.Context(), hub))
		cancel()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
		w := httptest.NewRecorder()
		h.ReadyZ(w, r)
	}
	hub.Flush(time.Second)

	require.Empty(t, ft.events, "aborted probes must not page")
	h.readyzR2.mu.Lock()
	fails := h.readyzR2.fails
	h.readyzR2.mu.Unlock()
	require.Zero(t, fails, "aborted probes must not advance the strike counter")
}
