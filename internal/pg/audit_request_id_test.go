package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordAudit_PersistsRequestID(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordAudit(ctx, AuditEvent{
		Actor:     "alice",
		Action:    "repo.approve",
		Outcome:   "success",
		RequestID: "req_abc123",
		Detail:    map[string]any{"name": "live"},
	}))

	var got string
	require.NoError(t, repo.pool.QueryRow(ctx,
		`SELECT request_id FROM audit_log WHERE actor = 'alice'`).Scan(&got))
	assert.Equal(t, "req_abc123", got, "request_id correlates the durable row to the trace/access-log")
}

func TestAuditLog_ActorIndexExists(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	var exists bool
	require.NoError(t, repo.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE tablename = 'audit_log' AND indexname = 'audit_log_actor_idx')`).Scan(&exists))
	assert.True(t, exists, "per-actor dashboard queries must not seq-scan")
}
