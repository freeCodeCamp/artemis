package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
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
