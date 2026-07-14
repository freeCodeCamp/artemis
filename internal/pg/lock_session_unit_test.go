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

func (f *fakeSessionConn) Close(context.Context) error { f.closed++; return nil }

func TestLockSession_UnlockFailure_ClosesConnAndSurfaces(t *testing.T) {
	fc := &fakeSessionConn{failUnlock: true}
	s := &lockSession{conn: fc}

	err := s.WithSiteLock(context.Background(), "www.freecode.camp", func() error { return nil })

	require.Error(t, err, "a failed advisory unlock must surface, not be swallowed into a stranded lock")
	assert.Contains(t, err.Error(), "unlock")
	assert.GreaterOrEqual(t, fc.closed, 1,
		"on unlock failure the session conn is closed to force-release the advisory lock (no writer starvation for the rest of the GC run)")
}

func TestLockSession_UnlockSuccess_NoClose_NoError(t *testing.T) {
	fc := &fakeSessionConn{}
	s := &lockSession{conn: fc}

	err := s.WithSiteLock(context.Background(), "www.freecode.camp", func() error { return nil })

	require.NoError(t, err)
	assert.Equal(t, 0, fc.closed, "happy path keeps the session conn open for reuse across candidates")
}
