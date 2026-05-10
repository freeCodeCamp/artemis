package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

// newMiniredis returns a miniredis server seeded with the given
// password. The server lifetime is bound to t.Cleanup so each test
// gets a fresh, isolated instance.
func newMiniredis(t *testing.T, password string) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	if password != "" {
		mr.RequireAuth(password)
	}
	return mr
}

func TestNewStore_PingsValkey(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Ping(ctx))
}

func TestNewStore_AuthRequired(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "secret-pw")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Wrong password fails ping.
	_, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr(), Password: "wrong"})
	require.Error(t, err)

	// Right password succeeds.
	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr(), Password: "secret-pw"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Ping(ctx))
}

func TestNewStore_RejectsEmptyAddr(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := valkey.New(ctx, valkey.Config{})
	require.Error(t, err)
}

func TestStore_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "")
	ctx := context.Background()

	s, err := valkey.New(ctx, valkey.Config{Addr: mr.Addr()})
	require.NoError(t, err)

	require.NoError(t, s.Close())
	// Second Close on a redis.Client returns ErrClosed; both nil and
	// ErrClosed are acceptable — the contract is "safe to call".
	_ = s.Close()
}
