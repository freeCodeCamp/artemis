package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func stubCapture(t *testing.T) *[]string {
	t.Helper()
	var ops []string
	orig := captureBackground
	captureBackground = func(op string, err error) { ops = append(ops, op) }
	t.Cleanup(func() { captureBackground = orig })
	return &ops
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

func TestOnConcurrentMigrateErr_AbortedOnShutdownCancel(t *testing.T) {
	ops := stubCapture(t)
	logs := captureSlog(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	onConcurrentMigrateErr(ctx, context.Canceled)

	assert.Empty(t, *ops, "a shutdown-canceled concurrent migration must NOT escalate via captureBackground")
	assert.Contains(t, logs.String(), "pg.migrate.concurrent.aborted", "shutdown abort must log a distinct event")
}

func TestOnConcurrentMigrateErr_EscalatesRealError(t *testing.T) {
	ops := stubCapture(t)

	onConcurrentMigrateErr(context.Background(), errors.New("index build failed: disk full"))

	assert.Equal(t, []string{"pg.migrate.concurrent"}, *ops, "a genuine migration error (ctx live) must escalate")
}

func TestOnConcurrentMigrateErr_NilNoop(t *testing.T) {
	ops := stubCapture(t)
	onConcurrentMigrateErr(context.Background(), nil)
	assert.Empty(t, *ops, "no error means no escalation")
}
