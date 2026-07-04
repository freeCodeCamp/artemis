package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestReadyzDegraded_ValkeyDownHardFailsEvenIfPGUp(t *testing.T) {
	h := &Handlers{
		Health:   &fakeHealth{err: errors.New("valkey down")},
		R2:       newFakeR2(),
		PGHealth: &fakeHealth{},
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "Valkey/R2 down = hard down even when PG ok")
}

func TestReadyZ_ValkeyDown_Returns503_ValkeyUnreachable(t *testing.T) {
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("dial tcp valkey:6379: i/o timeout")},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"valkey_unreachable"`)
}

func TestReadyZ_R2Down_Returns503_R2Unreachable(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("s3: bucket not found")
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"r2_unreachable"`)
}

func TestReadyZ_ValkeyFailureTakesPrecedenceOnDoubleFailure(t *testing.T) {
	r2 := newFakeR2()
	r2.listErr = errors.New("r2 also down")
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("valkey down")},
		R2:     r2,
	}

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"valkey_unreachable"`)
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

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Empty(t, ft.events, "a single readyz failure is a blip (streak < threshold) — must not page")
}

func TestReadyz_SustainedHang_PagesAtThreshold(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	r2.listErr = fmt.Errorf("r2 has_prefix: %w", context.DeadlineExceeded) // wedged/black-holed upstream
	h := &Handlers{Health: &fakeHealth{}, R2: r2}

	for i := 0; i < readyzPageThreshold+5; i++ {
		require.Equal(t, http.StatusServiceUnavailable, readyzProbe(t, h, hub).Code)
	}
	hub.Flush(time.Second)

	require.Len(t, ft.events, 1,
		"a SUSTAINED hang must page EXACTLY ONCE at the threshold crossing (edge-triggered) — not on every probe for the whole outage")
	assert.Equal(t, "r2.has_prefix", ft.events[0].Tags["op"])
	assert.Equal(t, []string{"readyz", "r2.has_prefix"}, ft.events[0].Fingerprint)
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

func TestReadyz_DualOutageThenValkeyHeals_R2StillPages(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	r2 := newFakeR2()
	health := &fakeHealth{}
	h := &Handlers{Health: health, R2: r2}

	health.err = errors.New("valkey down")
	r2.listErr = errors.New("r2 down")
	for i := 0; i < readyzPageThreshold+2; i++ {
		readyzProbe(t, h, hub) // valkey precedence masks r2; r2 streak overshoots threshold
	}
	health.err = nil
	for i := 0; i < 10; i++ {
		readyzProbe(t, h, hub) // real ongoing r2 outage, now unmasked
	}
	hub.Flush(time.Second)

	ops := map[string]int{}
	for _, e := range ft.events {
		ops[e.Tags["op"]]++
	}
	require.Equal(t, 1, ops["r2.has_prefix"],
		"an ongoing R2 outage must page once even after being masked by a valkey outage that healed — latch dead-zone fix")
}
