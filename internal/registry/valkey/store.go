// Package valkey is the Valkey-backed implementation of the artemis
// site registry. It serializes registrations through a single Valkey
// instance and uses pub-sub for cross-replica cache invalidation. The
// schema (HSET site:<slug> + SADD sites:all + PUBLISH registry.changed)
// is specified in rfc-gxy-cassiopeia-ga.md §B.
package valkey

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ChannelRegistryChanged is the pub-sub channel emitted on every
// state-mutating registry write. Subscribers receive the changed slug
// in the message body so they can invalidate scoped caches without
// re-reading the entire registry.
const ChannelRegistryChanged = "registry.changed"

// Config carries the wire credentials needed to dial Valkey. Address
// follows the host:port convention (no scheme prefix). Password is
// required by the production chart (AUTH-enabled); unauthenticated
// dev instances set it to the empty string.
type Config struct {
	Addr     string
	Password string
}

// Store is a thin wrapper around the go-redis client scoped to the
// registry use cases. The methods on Store are the only entry point
// the rest of the codebase has into Valkey — no go-redis types leak
// through the package boundary.
type Store struct {
	client *redis.Client
}

// New dials Valkey, verifies connectivity with a PING, and returns
// a ready Store. The caller must Close the store when done.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Addr == "" {
		return nil, errors.New("valkey: empty Addr")
	}
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("valkey: ping %s: %w", cfg.Addr, err)
	}
	return &Store{client: c}, nil
}

// Ping verifies the underlying connection. Cheap; safe to call on a
// liveness probe.
func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Close releases the underlying connection pool. Safe to call multiple
// times; subsequent calls return the same nil/error result as the first.
func (s *Store) Close() error {
	return s.client.Close()
}
