package valkey_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/reporequest"
	"github.com/freeCodeCamp/artemis/internal/reporequest/valkey"
)

func newStore(t *testing.T) *valkey.Store {
	t.Helper()
	mr := miniredis.RunT(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	var idN int64
	s.NewID = func() string { return fmt.Sprintf("req_%03d", atomic.AddInt64(&idN, 1)) }
	var clockN int64
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base.Add(time.Duration(atomic.AddInt64(&clockN, 1)) * time.Second) }
	return s
}

func sampleReq(name string) reporequest.Request {
	return reporequest.Request{
		Name:        name,
		Owner:       "freeCodeCamp-Universe",
		Visibility:  reporequest.VisibilityPrivate,
		Description: "a repo",
		RequestedBy: "octocat",
	}
}

func TestStore_CreateAndGet(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, sampleReq("my-repo"))
	require.NoError(t, err)
	assert.Equal(t, "req_001", created.ID)
	assert.Equal(t, reporequest.StatusPending, created.Status)

	got, err := s.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "my-repo", got.Name)
	assert.Equal(t, "freeCodeCamp-Universe", got.Owner)
	assert.Equal(t, reporequest.VisibilityPrivate, got.Visibility)
	assert.Equal(t, "octocat", got.RequestedBy)
	assert.Equal(t, created.CreatedAt.UTC(), got.CreatedAt.UTC())
}

func TestStore_GetNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.Get(context.Background(), "req_missing")
	assert.ErrorIs(t, err, reporequest.ErrNotFound)
}

func TestStore_CreateDuplicateName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.Create(ctx, sampleReq("dup"))
	require.NoError(t, err)
	_, err = s.Create(ctx, sampleReq("dup"))
	assert.ErrorIs(t, err, reporequest.ErrAlreadyExists)
}

func TestStore_RejectReleasesName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.Create(ctx, sampleReq("rel"))
	require.NoError(t, err)

	rejected, err := s.Reject(ctx, r.ID, "admin1", "not needed")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusRejected, rejected.Status)
	assert.Equal(t, "admin1", rejected.Approver)
	assert.Equal(t, "not needed", rejected.RejectReason)

	// name freed → a fresh request for the same name now succeeds.
	_, err = s.Create(ctx, sampleReq("rel"))
	require.NoError(t, err)
}

func TestStore_ApproveThenActive(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.Create(ctx, sampleReq("live"))
	require.NoError(t, err)

	approved, err := s.Approve(ctx, r.ID, "admin1")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusApproved, approved.Status)
	assert.Equal(t, "admin1", approved.Approver)

	active, err := s.MarkActive(ctx, r.ID, "https://github.com/freeCodeCamp-Universe/live")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusActive, active.Status)
	assert.Equal(t, "https://github.com/freeCodeCamp-Universe/live", active.URL)

	// name stays claimed after going active.
	_, err = s.Create(ctx, sampleReq("live"))
	assert.ErrorIs(t, err, reporequest.ErrAlreadyExists)
}

func TestStore_MarkFailedReleasesName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.Create(ctx, sampleReq("flaky"))
	require.NoError(t, err)
	_, err = s.Approve(ctx, r.ID, "admin1")
	require.NoError(t, err)

	failed, err := s.MarkFailed(ctx, r.ID, "boom")
	require.NoError(t, err)
	assert.Equal(t, reporequest.StatusFailed, failed.Status)
	assert.Equal(t, "boom", failed.Error)

	_, err = s.Create(ctx, sampleReq("flaky"))
	require.NoError(t, err, "failed creation must free the name for retry")
}

func TestStore_ApproveIsCASGuarded(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	r, err := s.Create(ctx, sampleReq("race"))
	require.NoError(t, err)

	const racers = 8
	var wins, notPending int32
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			_, err := s.Approve(ctx, r.ID, "admin")
			switch {
			case err == nil:
				atomic.AddInt32(&wins, 1)
			case errors.Is(err, reporequest.ErrNotPending):
				atomic.AddInt32(&notPending, 1)
			default:
				t.Errorf("unexpected approve error: %v", err)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&wins), "exactly one admin must win the approval")
	assert.Equal(t, int32(racers-1), atomic.LoadInt32(&notPending))
}

func TestStore_ListSortedByCreatedAt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, n := range []string{"c", "a", "b"} {
		_, err := s.Create(ctx, sampleReq(n))
		require.NoError(t, err)
	}
	list, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	// insertion order c,a,b with strictly advancing clock → same order.
	assert.Equal(t, "c", list[0].Name)
	assert.Equal(t, "a", list[1].Name)
	assert.Equal(t, "b", list[2].Name)
}
