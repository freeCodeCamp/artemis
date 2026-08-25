package pg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessionConn struct {
	failUnlock bool
	closed     int
	execs      []string
}

func (f *fakeSessionConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	if f.failUnlock && strings.Contains(sql, "pg_advisory_unlock") {
		return pgconn.CommandTag{}, errors.New("unlock: connection reset")
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeSessionConn) Ping(context.Context) error { return nil }

func (f *fakeSessionConn) Close(context.Context) error { f.closed++; return nil }

var errClosureVerdict = errors.New("the closure's own verdict")

func TestLockSession_UnlockFailure_ClosesConnWithoutPromoting(t *testing.T) {
	fc := &fakeSessionConn{failUnlock: true}
	s := &lockSession{conn: fc}

	err := s.WithSiteLock(context.Background(), "www.freecode.camp", func(context.Context) error { return nil })

	require.NoError(t, err,
		"closing the session conn releases every session-scoped advisory lock, so a failed unlock "+
			"is informational; promoting it makes every caller read committed work as a failure")
	assert.GreaterOrEqual(t, fc.closed, 1,
		"on unlock failure the session conn is closed to force-release the advisory lock (no writer starvation for the rest of the GC run)")
}

func TestLockSession_UnlockFailure_KeepsTheClosureError(t *testing.T) {
	fc := &fakeSessionConn{failUnlock: true}
	s := &lockSession{conn: fc}

	err := s.WithSiteLock(context.Background(), "www.freecode.camp", func(context.Context) error { return errClosureVerdict })

	require.ErrorIs(t, err, errClosureVerdict,
		"the closure's error is the caller's answer; a later unlock failure must not replace it")
}

func TestLockSession_UnlockSuccess_NoClose_NoError(t *testing.T) {
	fc := &fakeSessionConn{}
	s := &lockSession{conn: fc}

	err := s.WithSiteLock(context.Background(), "www.freecode.camp", func(context.Context) error { return nil })

	require.NoError(t, err)
	assert.Equal(t, 0, fc.closed, "happy path keeps the session conn open for reuse across candidates")
}
