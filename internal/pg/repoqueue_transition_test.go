package pg

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

func TestRepoQueue_MarkFailed(t *testing.T) {
	ctx := context.Background()
	q := newTestRepoQueue(t)

	r, err := q.Create(ctx, req("x"))
	require.NoError(t, err)
	_, err = q.Approve(ctx, r.ID, "admin")
	require.NoError(t, err)

	f, err := q.MarkFailed(ctx, r.ID, "build broke")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusFailed, f.Status)
	assert.Equal(t, "build broke", f.Error)

	got, err := q.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusFailed, got.Status)
	assert.Equal(t, "build broke", got.Error)
}

func TestRepoQueue_MarkFailed_NotApproved(t *testing.T) {
	ctx := context.Background()
	q := newTestRepoQueue(t)

	r, err := q.Create(ctx, req("y"))
	require.NoError(t, err)

	_, err = q.MarkFailed(ctx, r.ID, "ignored")
	assert.ErrorIs(t, err, reporequest.ErrNotPending,
		"MarkFailed on a pending (un-approved) request is blocked by the CAS guard")

	got, err := q.Get(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusPending, got.Status, "rejected transition leaves status unchanged")
	assert.Empty(t, got.Error)
}

func TestRepoQueue_TransitionMismatchGuards(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		setup   func(t *testing.T, q *RepoQueue, id string)
		verb    func(q *RepoQueue, id string) (reporequest.Request, error)
		want    reporequest.Status
		wantErr error
	}{
		{
			name:    "MarkActive on pending (not approved)",
			setup:   func(t *testing.T, q *RepoQueue, id string) {},
			verb:    func(q *RepoQueue, id string) (reporequest.Request, error) { return q.MarkActive(ctx, id, "https://x") },
			want:    reporequest.StatusPending,
			wantErr: reporequest.ErrNotPending,
		},
		{
			name: "MarkActive on failed",
			setup: func(t *testing.T, q *RepoQueue, id string) {
				t.Helper()
				_, err := q.Approve(ctx, id, "admin")
				require.NoError(t, err)
				_, err = q.MarkFailed(ctx, id, "broke")
				require.NoError(t, err)
			},
			verb:    func(q *RepoQueue, id string) (reporequest.Request, error) { return q.MarkActive(ctx, id, "https://x") },
			want:    reporequest.StatusFailed,
			wantErr: reporequest.ErrNotPending,
		},
		{
			name: "MarkStale on approved (not active)",
			setup: func(t *testing.T, q *RepoQueue, id string) {
				t.Helper()
				_, err := q.Approve(ctx, id, "admin")
				require.NoError(t, err)
			},
			verb:    func(q *RepoQueue, id string) (reporequest.Request, error) { return q.MarkStale(ctx, id, "reason") },
			want:    reporequest.StatusApproved,
			wantErr: reporequest.ErrNotActive,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newTestRepoQueue(t)
			r, err := q.Create(ctx, req("app"))
			require.NoError(t, err)
			tc.setup(t, q, r.ID)

			_, err = tc.verb(q, r.ID)
			assert.ErrorIs(t, err, tc.wantErr)

			got, err := q.Get(ctx, r.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Status, "rejected transition leaves status unchanged")
		})
	}
}
