// Package valkey is the Valkey-backed implementation of the artemis
// site registry. It serializes registrations through a single Valkey
// instance and uses pub-sub for cross-replica cache invalidation.
//
// Wire schema:
//
//   - HSET  site:<slug>   — hash row per site (teams, created_at,
//     updated_at, created_by fields)
//   - SADD  sites:all     — index set of every registered slug
//   - PUBLISH registry.changed <slug> — fired on every state-mutating
//     write so subscribers can
//     invalidate scoped caches
package valkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

// ChannelRegistryChanged is the pub-sub channel emitted on every
// state-mutating registry write. Subscribers receive the changed slug
// in the message body so they can invalidate scoped caches without
// re-reading the entire registry.
const ChannelRegistryChanged = "registry.changed"

// keyAllSites is the Set index of every registered slug.
const keyAllSites = "sites:all"

// fieldTeams, fieldCreatedAt, fieldUpdatedAt, fieldCreatedBy are the
// hash field names. The literal strings are the wire contract; tests
// assert against them.
const (
	fieldTeams     = "teams"
	fieldCreatedAt = "created_at"
	fieldUpdatedAt = "updated_at"
	fieldCreatedBy = "created_by"
)

// Re-exports of the registry sentinel errors and Site type. Callers
// in this package use the local names so the registry import stays a
// boundary detail.
var (
	ErrAlreadyExists = registry.ErrAlreadyExists
	ErrNotFound      = registry.ErrNotFound
)

// Site aliases registry.Site for ergonomic in-package use.
type Site = registry.Site

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

	// Now is the clock used for created_at / updated_at fields. Tests
	// inject a deterministic clock; production uses time.Now.
	Now func() time.Time
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
	return &Store{client: c, Now: time.Now}, nil
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

// Subscribe returns a channel that receives the slug payload of every
// registry.changed event delivered to this connection. The channel
// closes when ctx is canceled or the underlying subscription returns
// an unrecoverable error. The pub-sub connection is hot when this
// function returns — calls made after that are guaranteed to be
// observed by the channel (no startup race).
//
// Callers are expected to consume promptly; the internal forwarder
// uses a small buffer (16). If the buffer fills, messages are
// dropped silently — pub-sub is fire-and-forget by design (artemis
// pairs this with a TTL-fallback cache for missed events).
func (s *Store) Subscribe(ctx context.Context) (<-chan string, error) {
	pubsub := s.client.Subscribe(ctx, ChannelRegistryChanged)
	// Receive blocks until the SUBSCRIBE confirmation arrives, so when
	// it returns we know the connection is registered with the server
	// and any subsequent PUBLISH will be delivered.
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	out := make(chan string, 16)
	go func() {
		defer close(out)
		defer func() { _ = pubsub.Close() }()
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- msg.Payload:
				default:
					// buffer full — drop; TTL fallback covers the miss
				}
			}
		}
	}()
	return out, nil
}

func (s *Store) Publish(ctx context.Context, slug string) error {
	return s.client.Publish(ctx, ChannelRegistryChanged, slug).Err()
}

func PublishOnChange(ctx context.Context, store *Store) func(slug string) {
	return func(slug string) {
		if err := store.Publish(ctx, slug); err != nil {
			slog.Warn("registry.publish.failed", "site", slug, "err", err)
		}
	}
}

// siteKey returns the hash key for a given slug. Defined in one place
// so the wire format (`site:<slug>`) cannot drift between methods.
func siteKey(slug string) string {
	return "site:" + slug
}

// Register writes a new site row atomically and publishes a
// registry.changed event on success. Returns ErrAlreadyExists if the
// slug is already in the index set; the existing row is left
// untouched. All concurrent Register calls for the same slug are
// serialized — exactly one succeeds, the rest return ErrAlreadyExists.
func (s *Store) Register(ctx context.Context, slug string, teams []string, createdBy string) (Site, error) {
	if slug == "" {
		return Site{}, errors.New("registry: empty slug")
	}
	now := s.Now().UTC()
	site := Site{
		Slug:      slug,
		Teams:     append([]string(nil), teams...),
		CreatedAt: now,
		UpdatedAt: now,
		CreatedBy: createdBy,
	}

	// WATCH+MULTI/EXEC gives us the SISMEMBER check + writes as one
	// optimistic transaction. Concurrent registrants either succeed
	// (first one) or trip the WATCH (rest) and re-read the index;
	// re-read sees the slug present and returns ErrAlreadyExists.
	txf := func(tx *redis.Tx) error {
		exists, err := tx.SIsMember(ctx, keyAllSites, slug).Result()
		if err != nil {
			return err
		}
		if exists {
			return ErrAlreadyExists
		}
		teamsJSON, err := json.Marshal(site.Teams)
		if err != nil {
			return fmt.Errorf("encode teams: %w", err)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, siteKey(slug),
				fieldTeams, string(teamsJSON),
				fieldCreatedAt, site.CreatedAt.Format(time.RFC3339Nano),
				fieldUpdatedAt, site.UpdatedAt.Format(time.RFC3339Nano),
				fieldCreatedBy, site.CreatedBy,
			)
			pipe.SAdd(ctx, keyAllSites, slug)
			pipe.Publish(ctx, ChannelRegistryChanged, slug)
			return nil
		})
		return err
	}

	for {
		err := s.client.Watch(ctx, txf, keyAllSites)
		switch {
		case err == nil:
			return site, nil
		case errors.Is(err, redis.TxFailedErr):
			// Optimistic lock failed; another writer touched
			// keyAllSites between our SISMEMBER and EXEC. Retry —
			// either we lose the race (and SISMEMBER returns
			// ErrAlreadyExists) or we win on the next pass.
			continue
		default:
			return Site{}, err
		}
	}
}

// UpdateTeams replaces the teams list for an existing slug, stamps
// updated_at to the store's clock, and publishes a registry.changed
// event. Returns ErrNotFound if the slug is not in the index set.
// Concurrent updates are serialized via WATCH+MULTI/EXEC on the row
// key; the loser of an optimistic-lock race retries and re-reads.
func (s *Store) UpdateTeams(ctx context.Context, slug string, teams []string) (Site, error) {
	if slug == "" {
		return Site{}, errors.New("registry: empty slug")
	}
	teamsCopy := append([]string(nil), teams...)
	now := s.Now().UTC()

	var resolved Site
	txf := func(tx *redis.Tx) error {
		exists, err := tx.SIsMember(ctx, keyAllSites, slug).Result()
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		// Read existing row so the response carries created_at +
		// created_by unchanged from the original Register call.
		values, err := tx.HGetAll(ctx, siteKey(slug)).Result()
		if err != nil {
			return err
		}
		existing, err := decodeSite(slug, values)
		if err != nil {
			return err
		}
		teamsJSON, err := json.Marshal(teamsCopy)
		if err != nil {
			return fmt.Errorf("encode teams: %w", err)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, siteKey(slug),
				fieldTeams, string(teamsJSON),
				fieldUpdatedAt, now.Format(time.RFC3339Nano),
			)
			pipe.Publish(ctx, ChannelRegistryChanged, slug)
			return nil
		})
		if err != nil {
			return err
		}
		resolved = Site{
			Slug:      slug,
			Teams:     teamsCopy,
			CreatedAt: existing.CreatedAt,
			UpdatedAt: now,
			CreatedBy: existing.CreatedBy,
		}
		return nil
	}

	for {
		err := s.client.Watch(ctx, txf, siteKey(slug))
		switch {
		case err == nil:
			return resolved, nil
		case errors.Is(err, redis.TxFailedErr):
			continue
		default:
			return Site{}, err
		}
	}
}

// Delete removes the slug's hash row + index-set member and
// publishes a registry.changed event. Returns ErrNotFound if the
// slug is absent. R2 deploy bytes are NOT touched — those age out
// via the post-GA cleanup cron.
func (s *Store) Delete(ctx context.Context, slug string) error {
	if slug == "" {
		return errors.New("registry: empty slug")
	}
	txf := func(tx *redis.Tx) error {
		exists, err := tx.SIsMember(ctx, keyAllSites, slug).Result()
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, siteKey(slug))
			pipe.SRem(ctx, keyAllSites, slug)
			pipe.Publish(ctx, ChannelRegistryChanged, slug)
			return nil
		})
		return err
	}

	for {
		err := s.client.Watch(ctx, txf, keyAllSites, siteKey(slug))
		switch {
		case err == nil:
			return nil
		case errors.Is(err, redis.TxFailedErr):
			continue
		default:
			return err
		}
	}
}

// TeamsForSite returns the authorized teams for a slug or
// ErrNotFound when the slug is absent. Callers MUST treat the slice
// as read-only; the package returns a fresh copy per call.
func (s *Store) TeamsForSite(ctx context.Context, slug string) ([]string, error) {
	site, err := s.GetSite(ctx, slug)
	if err != nil {
		return nil, err
	}
	return site.Teams, nil
}

// GetSite returns the full Site row or ErrNotFound. Used by the
// list endpoint to enumerate metadata; callers that only need the
// teams list should use TeamsForSite.
func (s *Store) GetSite(ctx context.Context, slug string) (Site, error) {
	values, err := s.client.HGetAll(ctx, siteKey(slug)).Result()
	if err != nil {
		return Site{}, err
	}
	// HGETALL on a missing key returns an empty map, not an error.
	// Cross-check the index set so a hash row deleted out-of-band
	// without SREM still surfaces as ErrNotFound (defense in depth).
	if len(values) == 0 {
		return Site{}, ErrNotFound
	}
	return decodeSite(slug, values)
}

// Sites returns every registered site, sorted by slug ascending. The
// implementation reads the index set then HGETALLs each row; for
// the expected ~100 → ~10K site range this is acceptable. If the
// fan-out becomes a hotspot, switch to a pipelined read.
func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	slugs, err := s.client.SMembers(ctx, keyAllSites).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(slugs)
	out := make([]Site, 0, len(slugs))
	for _, slug := range slugs {
		values, err := s.client.HGetAll(ctx, siteKey(slug)).Result()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			// Row deleted out-of-band between SMEMBERS and HGETALL.
			// Skip rather than fail the whole enumeration.
			continue
		}
		site, err := decodeSite(slug, values)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, nil
}

// decodeSite parses the raw hash fields back into a Site. Wire
// format (JSON teams, RFC3339Nano timestamps) is enforced here.
func decodeSite(slug string, values map[string]string) (Site, error) {
	site := Site{Slug: slug, CreatedBy: values[fieldCreatedBy]}
	if raw, ok := values[fieldTeams]; ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &site.Teams); err != nil {
			return Site{}, fmt.Errorf("decode teams for %q: %w", slug, err)
		}
	}
	if raw, ok := values[fieldCreatedAt]; ok && raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Site{}, fmt.Errorf("decode created_at for %q: %w", slug, err)
		}
		site.CreatedAt = t
	}
	if raw, ok := values[fieldUpdatedAt]; ok && raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Site{}, fmt.Errorf("decode updated_at for %q: %w", slug, err)
		}
		site.UpdatedAt = t
	}
	return site, nil
}
