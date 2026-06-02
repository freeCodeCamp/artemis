package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

func newTestRepoQueue(t *testing.T) *RepoQueue {
	t.Helper()
	repo := newTestRepo(t)
	return NewRepoQueue(&DB{Pool: repo.pool})
}

func req(name string) reporequest.Request {
	return reporequest.Request{Name: name, Owner: "freeCodeCamp-Universe", Visibility: reporequest.VisibilityPublic, RequestedBy: "alice"}
}

func TestRepoQueuePG(t *testing.T) {
	ctx := context.Background()
	q := newTestRepoQueue(t)

	created, err := q.Create(ctx, req("my-app"))
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusPending, created.Status)
	assert.NotEmpty(t, created.ID)

	_, err = q.Create(ctx, req("My-App"))
	assert.ErrorIs(t, err, reporequest.ErrAlreadyExists, "name claim is case-insensitive while pending")

	got, err := q.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-app", got.Name)
	_, err = q.Get(ctx, "req_absent")
	assert.ErrorIs(t, err, reporequest.ErrNotFound)

	approved, err := q.Approve(ctx, created.ID, "admin")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusApproved, approved.Status)
	assert.Equal(t, "admin", approved.Approver)

	_, err = q.Approve(ctx, created.ID, "admin2")
	assert.ErrorIs(t, err, reporequest.ErrNotPending, "double-approve blocked by CAS guard")

	active, err := q.MarkActive(ctx, created.ID, "https://github.com/o/my-app")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusActive, active.Status)
	assert.Equal(t, "https://github.com/o/my-app", active.URL)

	_, err = q.Create(ctx, req("my-app"))
	assert.ErrorIs(t, err, reporequest.ErrAlreadyExists, "active repo still holds the name claim")

	stale, err := q.MarkStale(ctx, created.ID, "repo deleted upstream")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusFailed, stale.Status)

	reused, err := q.Create(ctx, req("my-app"))
	require.NoError(t, err, "stale/failed releases the name claim -> name reusable")
	assert.Equal(t, reporequest.StatusPending, reused.Status)
}

func TestRepoQueuePG_RejectReleasesName(t *testing.T) {
	ctx := context.Background()
	q := newTestRepoQueue(t)

	r, err := q.Create(ctx, req("widget"))
	require.NoError(t, err)
	_, err = q.Reject(ctx, r.ID, "admin", "policy")
	require.NoError(t, err)

	_, err = q.Create(ctx, req("widget"))
	require.NoError(t, err, "rejected request releases the name claim")

	_, err = q.MarkActive(ctx, r.ID, "x")
	assert.ErrorIs(t, err, reporequest.ErrNotPending, "cannot activate a rejected request")
}

func TestRepoQueuePG_ListOrdered(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tick := 0
	q := newTestRepoQueue(t).WithClock(func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Minute)
	})
	for _, n := range []string{"a", "b", "c"} {
		_, err := q.Create(ctx, req(n))
		require.NoError(t, err)
	}
	list, err := q.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	names := []string{list[0].Name, list[1].Name, list[2].Name}
	assert.Equal(t, []string{"a", "b", "c"}, names, "ordered by created_at then id")

	require.NoError(t, q.Delete(ctx, list[0].ID))
	assert.ErrorIs(t, q.Delete(ctx, list[0].ID), reporequest.ErrNotFound)
}
