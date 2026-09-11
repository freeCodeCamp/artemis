package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const unreachableAddr = "127.0.0.1:1"

type staticSource struct{ sites []registry.Site }

func (s staticSource) Sites(context.Context) ([]registry.Site, error) {
	return append([]registry.Site(nil), s.sites...), nil
}

func TestNewUnverified_DoesNotDialAtConstruction(t *testing.T) {
	t.Parallel()

	store := valkey.NewUnverified(valkey.Config{Addr: unreachableAddr})
	require.NotNil(t, store)
	t.Cleanup(func() { _ = store.Close() })

	assert.Error(t, store.Ping(t.Context()),
		"construction must not verify; every call still reports the outage on its own")
}

func TestNewUnverified_EmptyAddrIsStillRefused(t *testing.T) {
	t.Parallel()

	assert.Nil(t, valkey.NewUnverified(valkey.Config{}),
		"an empty Addr is a configuration fault, not an outage")
}

func TestNewReaderFromSource_PostgresSourceBootsWithValkeyDown(t *testing.T) {
	t.Parallel()

	store := valkey.NewUnverified(valkey.Config{Addr: unreachableAddr})
	require.NotNil(t, store)
	t.Cleanup(func() { _ = store.Close() })

	src := staticSource{sites: []registry.Site{{Slug: "www", Teams: []string{"staff"}}}}
	r, err := valkey.NewReaderFromSource(t.Context(), src, store, time.Minute)

	require.NoError(t, err,
		"the source is Postgres, so a pod that restarts during a Valkey outage must boot, not crashloop")
	snap := r.Snapshot()
	assert.Equal(t, []sitekey.Slug{"www"}, snap.Sites())
	assert.Equal(t, []string{"staff"}, snap.TeamsForSite("www"))
}

func TestNewReaderFromSource_UnreadableSourceStillFailsBoot(t *testing.T) {
	t.Parallel()

	store := valkey.NewUnverified(valkey.Config{Addr: unreachableAddr})
	require.NotNil(t, store)
	t.Cleanup(func() { _ = store.Close() })

	_, err := valkey.NewReaderFromSource(t.Context(), store, store, time.Minute)

	require.Error(t, err,
		"before the Postgres cutover Valkey is the source, and a source that cannot be read is a hard boot failure")
}

func TestReaderFromSource_ResubscribesWhenValkeyReturns(t *testing.T) {
	t.Parallel()

	mr := newMiniredis(t, "")
	addr := mr.Addr()
	mr.Close()

	store := valkey.NewUnverified(valkey.Config{Addr: addr})
	require.NotNil(t, store)
	t.Cleanup(func() { _ = store.Close() })

	src := staticSource{sites: []registry.Site{{Slug: "www", Teams: []string{"staff"}}}}
	r, err := valkey.NewReaderFromSource(t.Context(), src, store, 20*time.Millisecond)
	require.NoError(t, err)

	require.NoError(t, mr.Restart())
	eventually(t, 3*time.Second, "the TTL ticker must retry Subscribe so the pod stops running TTL-only once Valkey returns", func() bool {
		return r.Subscribed()
	})
}
