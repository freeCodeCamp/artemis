package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedAudit(t *testing.T, repo *Repo, events ...AuditEvent) {
	t.Helper()
	for _, e := range events {
		require.NoError(t, repo.RecordAudit(context.Background(), e))
	}
}

func TestListAudit_NewestFirstAndFiltered(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	seedAudit(t, repo,
		AuditEvent{Actor: "alice", Action: "repo.approve", Site: "", Outcome: "success", Detail: map[string]any{"name": "app-a"}},
		AuditEvent{Actor: "bob", Action: "site.promote", Site: "www", Outcome: "success"},
		AuditEvent{Actor: "alice", Action: "repo.reject", Outcome: "success", Detail: map[string]any{"name": "app-b"}},
	)

	all, err := repo.ListAudit(ctx, AuditFilter{})
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "repo.reject", all[0].Action, "newest first (DESC)")
	assert.Equal(t, "repo.approve", all[2].Action, "oldest last")
	assert.Equal(t, "app-b", all[0].Detail["name"], "detail round-trips")

	byActor, err := repo.ListAudit(ctx, AuditFilter{Actor: "alice"})
	require.NoError(t, err)
	require.Len(t, byActor, 2)
	for _, r := range byActor {
		assert.Equal(t, "alice", r.Actor)
	}

	byAction, err := repo.ListAudit(ctx, AuditFilter{Action: "site.promote"})
	require.NoError(t, err)
	require.Len(t, byAction, 1)
	assert.Equal(t, "bob", byAction[0].Actor)

	bySite, err := repo.ListAudit(ctx, AuditFilter{Site: "www"})
	require.NoError(t, err)
	require.Len(t, bySite, 1)
	assert.Equal(t, "site.promote", bySite[0].Action)
}

func TestListAudit_Pagination(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.RecordAudit(ctx, AuditEvent{Actor: "alice", Action: "deploy.finalize", Outcome: "success"}))
	}

	page1, err := repo.ListAudit(ctx, AuditFilter{Limit: 2, Offset: 0})
	require.NoError(t, err)
	require.Len(t, page1, 2)

	page2, err := repo.ListAudit(ctx, AuditFilter{Limit: 2, Offset: 2})
	require.NoError(t, err)
	require.Len(t, page2, 2)

	assert.NotEqual(t, page1[0].ID, page2[0].ID, "pages do not overlap")
}

func TestListAudit_SinceFilter(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.RecordAudit(ctx, AuditEvent{Actor: "alice", Action: "site.delete", Outcome: "success"}))

	future, err := repo.ListAudit(ctx, AuditFilter{Since: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, future, "since in the future excludes all past rows")

	past, err := repo.ListAudit(ctx, AuditFilter{Since: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, past, 1)
}
