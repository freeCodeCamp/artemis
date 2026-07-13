package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditLog_AppendOnly_DBEnforced(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordAudit(ctx, AuditEvent{
		Actor:    "alice",
		Action:   "site.delete",
		Site:     "www",
		DeployID: "",
		Outcome:  "success",
		Detail:   map[string]any{"reason": "operator request"},
	}))

	_, err := repo.pool.Exec(ctx, `UPDATE audit_log SET actor = 'mallory'`)
	require.Error(t, err, "UPDATE on audit_log must be rejected by the DB (append-only, V5)")
	assert.Contains(t, err.Error(), "append-only")

	_, err = repo.pool.Exec(ctx, `DELETE FROM audit_log`)
	require.Error(t, err, "DELETE on audit_log must be rejected by the DB (append-only, V5)")
	assert.Contains(t, err.Error(), "append-only")

	_, err = repo.pool.Exec(ctx, `TRUNCATE audit_log`)
	require.Error(t, err, "TRUNCATE on audit_log must be rejected by the DB (a row-level UPDATE/DELETE guard does not fire on TRUNCATE, V5)")
	assert.Contains(t, err.Error(), "append-only")

	var n int
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n))
	assert.Equal(t, 1, n, "the row survives the rejected mutations")
}

func TestRecordAudit_NilDetailDefaultsToEmptyObject(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordAudit(ctx, AuditEvent{
		Actor:   "system:gc",
		Action:  "gc.purge",
		Outcome: "success",
	}))

	var detail string
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT detail::text FROM audit_log LIMIT 1`).Scan(&detail))
	assert.Equal(t, "{}", detail)
}
