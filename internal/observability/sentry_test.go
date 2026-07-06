package observability

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

// recordingTransport records every issue event the client tries to send,
// so a test can assert the slog bridge never emits issues.
type recordingTransport struct{ events []*sentry.Event }

func (r *recordingTransport) Configure(sentry.ClientOptions)        {}
func (r *recordingTransport) SendEvent(e *sentry.Event)             { r.events = append(r.events, e) }
func (r *recordingTransport) Flush(time.Duration) bool              { return true }
func (r *recordingTransport) FlushWithContext(context.Context) bool { return true }
func (r *recordingTransport) Close()                                {}

func TestInit_DisabledWhenNoDSN(t *testing.T) {
	flush, enabled, err := Init(Config{DSN: ""})
	require.NoError(t, err)
	require.False(t, enabled)
	require.NotNil(t, flush)
	flush() // must be safe to call
}

func TestProbeSampleRate(t *testing.T) {
	const base = 0.3
	for _, name := range []string{"GET /healthz", "GET /readyz"} {
		require.Zero(t, probeSampleRate(name, base), name)
	}
	require.InDelta(t, base, probeSampleRate("POST /api/deploy/init", base), 1e-9)
	require.InDelta(t, base, probeSampleRate("", base), 1e-9)
}

func TestScrubEvent_StripsSecrets(t *testing.T) {
	event := &sentry.Event{
		Request: &sentry.Request{
			Headers: map[string]string{
				"Authorization":       "Bearer ghp_secret",
				"Cookie":              "session=abc",
				"Proxy-Authorization": "Basic xxx",
				"X-Forwarded-For":     "1.2.3.4",
				"X-Request-Id":        "req-123",
				"Content-Type":        "application/json",
			},
			Cookies: "session=abc",
			Data:    "raw artifact bytes",
		},
	}

	got := scrubEvent(event, nil)

	require.NotContains(t, got.Request.Headers, "Authorization")
	require.NotContains(t, got.Request.Headers, "Cookie")
	require.NotContains(t, got.Request.Headers, "Proxy-Authorization")
	require.NotContains(t, got.Request.Headers, "X-Forwarded-For")
	require.Equal(t, "req-123", got.Request.Headers["X-Request-Id"], "reqID join key kept")
	require.Equal(t, "application/json", got.Request.Headers["Content-Type"])
	require.Empty(t, got.Request.Cookies)
	require.Empty(t, got.Request.Data)
}

func TestScrubEvent_NilSafe(t *testing.T) {
	require.Nil(t, scrubEvent(nil, nil))
	require.NotNil(t, scrubEvent(&sentry.Event{}, nil)) // no Request, no panic
}

// recordingHandler records the messages it handles, to assert fan-out.
type recordingHandler struct{ msgs *[]string }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message)
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

// TestNewSlogHandler_NeverEmitsIssues is the regression guard for the
// EventLevel double-capture footgun: a slog.Error routed through the
// bridge must produce a Sentry LOG, never an issue. If EventLevel
// regresses to its nil default ({Error,Fatal}) this test sees an issue
// event and fails.
func TestNewSlogHandler_NeverEmitsIssues(t *testing.T) {
	rt := &recordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@example.test/1",
		Transport: rt,
	})
	require.NoError(t, err)
	hub := sentry.NewHub(client, sentry.NewScope())
	ctx := sentry.SetHubOnContext(context.Background(), hub)

	logger := slog.New(NewSlogHandler(slog.LevelInfo))
	logger.ErrorContext(ctx, "boom")
	hub.Flush(time.Second)

	require.Empty(t, rt.events, "slog records must become logs, never issues")
}

func TestNewSlogHandler_GatesByMinLevel(t *testing.T) {
	h := NewSlogHandler(slog.LevelWarn)
	require.False(t, h.Enabled(context.Background(), slog.LevelInfo), "info dropped at warn min")
	require.True(t, h.Enabled(context.Background(), slog.LevelError), "error kept at warn min")
}

func TestNewMultiHandler_FansOut(t *testing.T) {
	var a, b []string
	logger := slog.New(NewMultiHandler(recordingHandler{&a}, nil, recordingHandler{&b}))

	logger.Info("hello")

	require.Equal(t, []string{"hello"}, a)
	require.Equal(t, []string{"hello"}, b, "nil handler skipped, both real handlers receive")
}
