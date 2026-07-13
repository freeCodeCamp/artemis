package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &m))
	return m
}

func TestLogHandler_InjectsScopeAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil)))

	sc := telemetry.New("req-1")
	sc.SetActor("alice")
	sc.SetResource("www", "d-9")
	ctx := telemetry.NewContext(context.Background(), sc)

	log.InfoContext(ctx, "deploy.finalize.ok")

	m := decodeLine(t, &buf)
	assert.Equal(t, "req-1", m["request_id"])
	assert.Equal(t, "alice", m["actor"])
	assert.Equal(t, "www", m["site"])
	assert.Equal(t, "d-9", m["deploy_id"])
}

func TestLogHandler_OmitsEmptyScope(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil)))

	log.InfoContext(context.Background(), "boot.start")

	m := decodeLine(t, &buf)
	_, hasReq := m["request_id"]
	assert.False(t, hasReq, "no request_id when scope absent")
	_, hasActor := m["actor"]
	assert.False(t, hasActor)
	_, hasTrace := m["trace_id"]
	assert.False(t, hasTrace, "no trace_id when no active span")
}

func TestLogHandler_InjectsTraceIDs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil)))

	span := sentry.StartSpan(context.Background(), "test.op")

	log.InfoContext(span.Context(), "some.event")

	m := decodeLine(t, &buf)
	assert.Equal(t, span.TraceID.String(), m["trace_id"])
	assert.Equal(t, span.SpanID.String(), m["span_id"])
}

func TestLogHandler_PreservesCallerAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil)))

	sc := telemetry.New("req-2")
	ctx := telemetry.NewContext(context.Background(), sc)
	log.InfoContext(ctx, "outbox.enqueue.failed", "err", "boom")

	m := decodeLine(t, &buf)
	assert.Equal(t, "req-2", m["request_id"])
	assert.Equal(t, "boom", m["err"])
}

func TestLogHandler_WithAttrs_KeepsScopeInjection(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil))).With("component", "relay")

	sc := telemetry.New("req-w")
	sc.SetActor("alice")
	ctx := telemetry.NewContext(context.Background(), sc)
	log.InfoContext(ctx, "msg")

	m := decodeLine(t, &buf)
	assert.Equal(t, "relay", m["component"], "WithAttrs attr present")
	assert.Equal(t, "req-w", m["request_id"], "scope injection survives the WithAttrs rewrap")
	assert.Equal(t, "alice", m["actor"])
}

func TestLogHandler_WithGroup_KeepsScopeInjection(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(telemetry.NewLogHandler(slog.NewJSONHandler(&buf, nil))).WithGroup("g")

	sc := telemetry.New("req-g")
	ctx := telemetry.NewContext(context.Background(), sc)
	log.InfoContext(ctx, "msg", "k", "v")

	m := decodeLine(t, &buf)
	g, ok := m["g"].(map[string]any)
	require.True(t, ok, "group object present; got=%v", m)
	assert.Equal(t, "v", g["k"], "caller attr under the group")
	assert.Equal(t, "req-g", g["request_id"], "scope injection survives the WithGroup rewrap")
}
