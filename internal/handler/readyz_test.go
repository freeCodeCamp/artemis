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

func TestReadyz_HardUpstreamFailure_CapturesSentry(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	h := &Handlers{
		Health: &fakeHealth{err: errors.New("dial tcp valkey:6379: connection refused")},
		R2:     newFakeR2(),
	}

	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Len(t, ft.events, 1, "a hard (non-transient) readyz upstream failure is a real outage — must page Sentry, the only alerting channel")
	assert.Equal(t, "valkey.ping", ft.events[0].Tags["op"])
	assert.Equal(t, []string{"readyz", "valkey.ping"}, ft.events[0].Fingerprint)
}

func TestReadyz_TransientProbeDeadline_NoSentry(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	r2 := newFakeR2()
	r2.listErr = fmt.Errorf("r2 has_prefix: %w", context.DeadlineExceeded)
	h := &Handlers{
		Health: &fakeHealth{},
		R2:     r2,
	}

	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	h.ReadyZ(w, r)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Empty(t, ft.events, "5s-probe DeadlineExceeded is the ARTEMIS-1 transient-blip class — must NOT create a Sentry issue")
}
