package telemetry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type spanTransport struct{ events []*sentry.Event }

func (s *spanTransport) Configure(sentry.ClientOptions)        {}
func (s *spanTransport) SendEvent(e *sentry.Event)             { s.events = append(s.events, e) }
func (s *spanTransport) Flush(time.Duration) bool              { return true }
func (s *spanTransport) FlushWithContext(context.Context) bool { return true }
func (s *spanTransport) Close()                                {}

func newTracingHub(t *testing.T) (*sentry.Hub, *spanTransport) {
	t.Helper()
	tr := &spanTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.test/1",
		Transport:        tr,
		EnableTracing:    true,
		TracesSampleRate: 1.0,
	})
	require.NoError(t, err)
	return sentry.NewHub(client, sentry.NewScope()), tr
}

func TestWithSpan_EmitsChildSpanOnSampledTx(t *testing.T) {
	hub, tr := newTracingHub(t)
	ctx := sentry.SetHubOnContext(context.Background(), hub)
	tx := sentry.StartTransaction(ctx, "test-tx")

	err := telemetry.WithSpan(tx.Context(), "r2.put.test", func(context.Context) error { return nil })
	require.NoError(t, err)
	tx.Finish()

	require.Len(t, tr.events, 1)
	ops := make([]string, 0)
	for _, sp := range tr.events[0].Spans {
		ops = append(ops, sp.Op)
	}
	assert.Contains(t, ops, "r2.put.test", "child span emitted with the op name")

	for _, sp := range tr.events[0].Spans {
		if sp.Op == "r2.put.test" {
			assert.NotEqual(t, sentry.SpanStatusInternalError, sp.Status, "success path does not mark the span errored")
		}
	}
}

func TestWithSpan_PropagatesError(t *testing.T) {
	hub, tr := newTracingHub(t)
	ctx := sentry.SetHubOnContext(context.Background(), hub)
	tx := sentry.StartTransaction(ctx, "test-tx")

	want := errors.New("boom")
	got := telemetry.WithSpan(tx.Context(), "r2.put.test", func(context.Context) error { return want })
	tx.Finish()

	assert.ErrorIs(t, got, want, "WithSpan returns the wrapped fn error verbatim")

	require.Len(t, tr.events, 1)
	var errored *sentry.Span
	for _, sp := range tr.events[0].Spans {
		if sp.Op == "r2.put.test" {
			errored = sp
			break
		}
	}
	require.NotNil(t, errored, "child span emitted on the error path")
	assert.Equal(t, sentry.SpanStatusInternalError, errored.Status, "error path marks the span status InternalError")
}
