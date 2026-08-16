package pg

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestIsConnClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bare ErrConnClosed", pgconn.ErrConnClosed, true},
		{"wrapped ErrConnClosed", fmt.Errorf("relay publish: %w", pgconn.ErrConnClosed), true},
		{"doubly wrapped", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", pgconn.ErrConnClosed)), true},
		{"unrelated error", errors.New("conn closed"), false},
		{"pg error", &pgconn.PgError{Code: "57P03"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsConnClosed(tc.err))
		})
	}
}

func TestRegistryStore_WithClock(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)
	s := (&RegistryStore{}).WithClock(func() time.Time { return fixed })
	require.Equal(t, fixed, s.now())
}

func TestRepoQueue_WithClockAndIDGen(t *testing.T) {
	t.Parallel()

	fixed := time.Date(2026, 8, 16, 4, 0, 0, 0, time.UTC)
	q := (&RepoQueue{}).WithClock(func() time.Time { return fixed }).WithIDGen(func() string { return "req_stub" })
	require.Equal(t, fixed, q.now())
	require.Equal(t, "req_stub", q.newID())
}

func TestDefaultRepoRequestID(t *testing.T) {
	t.Parallel()

	first := defaultRepoRequestID()
	require.True(t, strings.HasPrefix(first, "req_"), "got %q", first)
	require.Len(t, first, len("req_")+20)
	require.NotEqual(t, first, defaultRepoRequestID())
}
