package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

// fakeTransport records every event the client tries to deliver so tests
// can assert what would have been sent to Sentry, with no network.
type fakeTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (f *fakeTransport) Configure(sentry.ClientOptions) {}
func (f *fakeTransport) SendEvent(e *sentry.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}
func (f *fakeTransport) Flush(time.Duration) bool              { return true }
func (f *fakeTransport) FlushWithContext(context.Context) bool { return true }
func (f *fakeTransport) Close()                                {}

func newHubWithTransport(t *testing.T) (*sentry.Hub, *fakeTransport) {
	t.Helper()
	ft := &fakeTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.test/1",
		Transport:        ft,
		AttachStacktrace: true,
	})
	require.NoError(t, err)
	return sentry.NewHub(client, sentry.NewScope()), ft
}

func TestWriteUpstreamError_CapturesWithOpAndFingerprint(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/deploy/x/upload", nil)
	rec := httptest.NewRecorder()

	writeUpstreamError(rec, req, http.StatusBadGateway, "r2_put_failed", "r2.put.upload", errors.New("boom from r2"))
	hub.Flush(time.Second)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "upstream call failed", "client sees only the opaque message")
	require.NotContains(t, rec.Body.String(), "boom from r2", "raw upstream error never reaches the client")

	require.Len(t, ft.events, 1)
	ev := ft.events[0]
	require.Equal(t, "r2.put.upload", ev.Tags["op"])
	require.Equal(t, "r2_put_failed", ev.Tags["error_code"])
	require.Equal(t, []string{"upstream", "r2.put.upload"}, ev.Fingerprint)
}

func TestWriteUpstreamError_NoHubNoPanic(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sites", nil) // no hub on context
	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		writeUpstreamError(rec, req, http.StatusBadGateway, "x", "op.x", errors.New("e"))
	})
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestRecoverer_CapturesPanic(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)

	h := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/whoami", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.GreaterOrEqual(t, len(ft.events), 1, "panic captured to Sentry")
}
