package pg

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deadConn struct {
	pings atomic.Int32
	alive atomic.Bool
}

func (d *deadConn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (d *deadConn) Ping(context.Context) error {
	d.pings.Add(1)
	if d.alive.Load() {
		return nil
	}
	return errors.New("conn closed")
}

func (d *deadConn) Close(context.Context) error { return nil }

func TestWithSiteLock_CancelsTheClosureWhenTheSessionDies(t *testing.T) {
	conn := &deadConn{}
	var lost atomic.Bool
	sess := &lockSession{conn: conn, onLost: func() { lost.Store(true) }, heartbeat: 20 * time.Millisecond}

	start := time.Now()
	err := sess.WithSiteLock(context.Background(), sitekey.Dirname("www.freecode.camp"), func() error {
		for time.Since(start) < 500*time.Millisecond {
			if lost.Load() {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("the closure ran to completion with no lock and nothing noticed")
	})

	require.NoError(t, err)
	assert.True(t, lost.Load(),
		"postgres releases every advisory lock the instant its backend dies; a closure that keeps "+
			"running past that point has no mutual exclusion at all")
	assert.Positive(t, conn.pings.Load(), "the session must actually probe, not assume")
}

func TestWithSiteLock_LeavesALiveSessionAlone(t *testing.T) {
	conn := &deadConn{}
	conn.alive.Store(true)
	var lost atomic.Bool
	sess := &lockSession{conn: conn, onLost: func() { lost.Store(true) }, heartbeat: 20 * time.Millisecond}

	err := sess.WithSiteLock(context.Background(), sitekey.Dirname("www.freecode.camp"), func() error {
		time.Sleep(80 * time.Millisecond)
		return nil
	})

	require.NoError(t, err)
	assert.False(t, lost.Load(), "a healthy session must not be torn down by its own watchdog")
}

func TestWithSiteLock_NoHookIsANoOp(t *testing.T) {
	conn := &deadConn{}
	sess := &lockSession{conn: conn, heartbeat: 20 * time.Millisecond}

	err := sess.WithSiteLock(context.Background(), sitekey.Dirname("www.freecode.camp"), func() error { return nil })

	require.NoError(t, err)
	assert.Zero(t, conn.pings.Load(), "without a hook there is nobody to tell, so do not pay for the probe")
}

type slowPingConn struct {
	deadConn
	inFlight atomic.Bool
	overlap  atomic.Bool
}

func (c *slowPingConn) Ping(ctx context.Context) error {
	c.inFlight.Store(true)
	defer c.inFlight.Store(false)
	c.pings.Add(1)
	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
	}
	return nil
}

func (c *slowPingConn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if c.inFlight.Load() {
		c.overlap.Store(true)
	}
	return pgconn.CommandTag{}, nil
}

func TestWithSiteLock_UnlockNeverRunsBesideAnInFlightPing(t *testing.T) {
	conn := &slowPingConn{}
	sess := &lockSession{conn: conn, onLost: func() {}, heartbeat: 10 * time.Millisecond}

	err := sess.WithSiteLock(context.Background(), sitekey.Dirname("www.freecode.camp"), func() error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})

	require.NoError(t, err)
	assert.False(t, conn.overlap.Load(),
		"*pgx.Conn is not safe for concurrent use; an unlock racing a heartbeat ping returns "+
			"conn busy and forces the session closed")
}

func TestNewLockSession_CarriesNoHookWhenTheRepoHasNoneRegistered(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	sess, err := repo.NewLockSession(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { sess.Close(ctx) })

	inner, ok := sess.(*lockSession)
	require.True(t, ok)
	assert.Nil(t, inner.onLost,
		"a method value is never nil, so wrapping the hook made every site lock pay for a heartbeat "+
			"nobody could receive")
}

func TestNewLockSession_CarriesTheHookTheRepoRegistered(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	var paged atomic.Bool
	repo.OnLockSessionLost(func() { paged.Store(true) })

	sess, err := repo.NewLockSession(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { sess.Close(ctx) })

	inner, ok := sess.(*lockSession)
	require.True(t, ok)
	require.NotNil(t, inner.onLost)
	inner.onLost()
	assert.True(t, paged.Load())
}
