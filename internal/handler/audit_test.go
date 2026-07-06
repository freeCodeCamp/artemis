package handler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAudit struct {
	events []pg.AuditEvent
	err    error
}

func (f *fakeAudit) RecordAudit(ctx context.Context, e pg.AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func TestAudit_RecordsEvent(t *testing.T) {
	fa := &fakeAudit{}
	h := &Handlers{Audit: fa}
	h.audit(context.Background(), pg.AuditEvent{Action: "site.delete", Actor: "alice", Outcome: "success"})

	require.Len(t, fa.events, 1)
	assert.Equal(t, "alice", fa.events[0].Actor)
}

func TestAudit_FireAndLogOnFailure(t *testing.T) {
	logs := captureAccessLog(t)
	h := &Handlers{Audit: &fakeAudit{err: errors.New("db down")}}
	require.NotPanics(t, func() {
		h.audit(context.Background(), pg.AuditEvent{Action: "site.delete", Actor: "alice", Outcome: "success"})
	})
	assert.Equal(t, slog.LevelError, logs.levelOf(t, "audit.write.failed"),
		"a failed audit write must surface as an error log (never fails the response)")
}

func TestAudit_DetachedFromRequestCancellation(t *testing.T) {
	fa := &fakeAudit{}
	h := &Handlers{Audit: fa}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.audit(ctx, pg.AuditEvent{Action: "site.delete", Actor: "alice", Outcome: "success"})

	require.Len(t, fa.events, 1, "audit runs on a ctx detached from the cancelled request")
}

func TestAudit_NilStoreNoOp(t *testing.T) {
	h := &Handlers{}
	require.NotPanics(t, func() {
		h.audit(context.Background(), pg.AuditEvent{Action: "x", Actor: "y", Outcome: "success"})
	})
}
