// Package valkey is the Valkey-backed implementation of the repo-request
// queue. It mirrors the site-registry store's WATCH/MULTI optimistic-
// transaction discipline so concurrent admins can never double-resolve a
// request: the approve/reject transition is a compare-and-set guarded on
// the row's pending status (dossier §V3).
//
// Wire schema:
//
//   - HSET repo:<id>     — hash row per request
//   - SADD repos:all     — index set of every request id
//   - SADD repos:names   — set of repo names currently claimed (a name
//     is claimed while pending/approved/active; released on reject/fail)
package valkey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

const (
	keyAllRequests  = "repos:all"
	keyClaimedNames = "repos:names"
)

const (
	fieldName         = "name"
	fieldOwner        = "owner"
	fieldVisibility   = "visibility"
	fieldDescription  = "description"
	fieldTemplate     = "template"
	fieldStatus       = "status"
	fieldURL          = "url"
	fieldError        = "error"
	fieldRequestedBy  = "requested_by"
	fieldApprover     = "approver"
	fieldRejectReason = "reject_reason"
	fieldCreatedAt    = "created_at"
	fieldUpdatedAt    = "updated_at"
)

// Re-exports so callers in this package stay decoupled from the domain
// import path.
type Request = reporequest.Request

var (
	ErrNotFound      = reporequest.ErrNotFound
	ErrAlreadyExists = reporequest.ErrAlreadyExists
	ErrNotPending    = reporequest.ErrNotPending
)

// Config carries the Valkey connection details.
type Config struct {
	Addr     string
	Password string
}

// Store is the repo-request queue backed by a go-redis client.
type Store struct {
	client *redis.Client

	// Now stamps created_at / updated_at. Tests inject a deterministic
	// clock; production uses time.Now.
	Now func() time.Time
	// NewID mints request ids. Tests inject a deterministic generator;
	// production uses a crypto/rand-backed "req_<hex>".
	NewID func() string
}

// New dials Valkey, verifies connectivity, and returns a ready Store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Addr == "" {
		return nil, errors.New("reporequest/valkey: empty Addr")
	}
	c := redis.NewClient(&redis.Options{Addr: cfg.Addr, Password: cfg.Password})
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("reporequest/valkey: ping %s: %w", cfg.Addr, err)
	}
	return &Store{client: c, Now: time.Now, NewID: defaultNewID}, nil
}

// NewWithClient wraps an existing go-redis client (lets main.go share one
// connection pool across the site registry and repo-request stores).
// Returns an error on a nil client rather than deferring to a nil-pointer
// panic on the first store call.
func NewWithClient(c *redis.Client) (*Store, error) {
	if c == nil {
		return nil, errors.New("reporequest/valkey: nil client")
	}
	return &Store{client: c, Now: time.Now, NewID: defaultNewID}, nil
}

// Ping verifies the connection. Cheap; safe on a liveness probe.
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close releases the connection pool.
func (s *Store) Close() error { return s.client.Close() }

func defaultNewID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "req_" + hex.EncodeToString(b[:])
}

func reqKey(id string) string { return "repo:" + id }

// nameClaimKey normalizes a repo name for the dedupe claim set. GitHub
// repo names are case-insensitive for uniqueness, so "MyRepo" and
// "myrepo" must collide in the queue — claim on the lowercased name
// (the row still stores the requester's original casing for creation).
func nameClaimKey(name string) string { return strings.ToLower(name) }

// Create writes a new pending request. Returns ErrAlreadyExists if a
// request for the same repo name is already claimed (pending/approved/
// active). The name-claim check + writes run in one optimistic
// transaction so concurrent submits of the same name resolve to exactly
// one winner.
func (s *Store) Create(ctx context.Context, req Request) (Request, error) {
	if req.Name == "" {
		return Request{}, errors.New("reporequest/valkey: empty name")
	}
	now := s.Now().UTC()
	req.ID = s.NewID()
	req.Status = reporequest.StatusPending
	req.CreatedAt = now
	req.UpdatedAt = now

	txf := func(tx *redis.Tx) error {
		claimed, err := tx.SIsMember(ctx, keyClaimedNames, nameClaimKey(req.Name)).Result()
		if err != nil {
			return err
		}
		if claimed {
			return ErrAlreadyExists
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, reqKey(req.ID), encodeFields(req)...)
			pipe.SAdd(ctx, keyAllRequests, req.ID)
			pipe.SAdd(ctx, keyClaimedNames, nameClaimKey(req.Name))
			return nil
		})
		return err
	}

	if err := s.watch(ctx, txf, keyClaimedNames); err != nil {
		return Request{}, err
	}
	return req, nil
}

// Get returns a single request or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (Request, error) {
	vals, err := s.client.HGetAll(ctx, reqKey(id)).Result()
	if err != nil {
		return Request{}, err
	}
	if len(vals) == 0 {
		return Request{}, ErrNotFound
	}
	return decodeRequest(id, vals)
}

// List returns every request sorted by created_at ascending (id as tie-
// breaker). Filtering by status / requester is the handler's concern.
func (s *Store) List(ctx context.Context) ([]Request, error) {
	ids, err := s.client.SMembers(ctx, keyAllRequests).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(ids))
	for _, id := range ids {
		vals, err := s.client.HGetAll(ctx, reqKey(id)).Result()
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			continue // deleted out-of-band between SMEMBERS and HGETALL
		}
		req, err := decodeRequest(id, vals)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// Approve flips a pending request to approved and records the approver.
// CAS guard: a request that is no longer pending returns ErrNotPending,
// so only one of several racing admins wins (dossier §V3). The name
// remains claimed (the repo is about to be created).
func (s *Store) Approve(ctx context.Context, id, approver string) (Request, error) {
	return s.mutate(ctx, id, func(cur Request) (Request, bool, error) {
		if !cur.Status.CanResolve() {
			return Request{}, false, ErrNotPending
		}
		cur.Status = reporequest.StatusApproved
		cur.Approver = approver
		return cur, false, nil
	})
}

// Reject flips a pending request to rejected, records the approver +
// reason, and releases the name claim. CAS-guarded like Approve.
func (s *Store) Reject(ctx context.Context, id, approver, reason string) (Request, error) {
	return s.mutate(ctx, id, func(cur Request) (Request, bool, error) {
		if !cur.Status.CanResolve() {
			return Request{}, false, ErrNotPending
		}
		cur.Status = reporequest.StatusRejected
		cur.Approver = approver
		cur.RejectReason = reason
		return cur, true, nil // release name
	})
}

// MarkActive records a successful repo creation: approved → active with
// the repo URL. The name stays claimed (the repo now exists).
func (s *Store) MarkActive(ctx context.Context, id, url string) (Request, error) {
	return s.mutate(ctx, id, func(cur Request) (Request, bool, error) {
		if cur.Status != reporequest.StatusApproved {
			return Request{}, false, ErrNotPending
		}
		cur.Status = reporequest.StatusActive
		cur.URL = url
		return cur, false, nil
	})
}

// MarkFailed records a failed repo creation: approved → failed with the
// error message, releasing the name so the request can be retried.
func (s *Store) MarkFailed(ctx context.Context, id, errMsg string) (Request, error) {
	return s.mutate(ctx, id, func(cur Request) (Request, bool, error) {
		if cur.Status != reporequest.StatusApproved {
			return Request{}, false, ErrNotPending
		}
		cur.Status = reporequest.StatusFailed
		cur.Error = errMsg
		return cur, true, nil // release name
	})
}

// mutate applies fn to the current row inside a WATCH/MULTI transaction.
// fn returns the next row, whether to release the name claim, and an
// error to abort. updated_at is stamped automatically.
func (s *Store) mutate(ctx context.Context, id string, fn func(Request) (Request, bool, error)) (Request, error) {
	var result Request
	txf := func(tx *redis.Tx) error {
		vals, err := tx.HGetAll(ctx, reqKey(id)).Result()
		if err != nil {
			return err
		}
		if len(vals) == 0 {
			return ErrNotFound
		}
		cur, err := decodeRequest(id, vals)
		if err != nil {
			return err
		}
		next, release, err := fn(cur)
		if err != nil {
			return err
		}
		next.UpdatedAt = s.Now().UTC()
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.HSet(ctx, reqKey(id), encodeFields(next)...)
			if release {
				pipe.SRem(ctx, keyClaimedNames, nameClaimKey(next.Name))
			}
			return nil
		})
		if err != nil {
			return err
		}
		result = next
		return nil
	}

	if err := s.watch(ctx, txf, reqKey(id)); err != nil {
		return Request{}, err
	}
	return result, nil
}

// watch runs txf under optimistic locking on the given keys, retrying on
// the redis.TxFailedErr optimistic-lock conflict.
func (s *Store) watch(ctx context.Context, txf func(*redis.Tx) error, keys ...string) error {
	for {
		err := s.client.Watch(ctx, txf, keys...)
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

func encodeFields(r Request) []any {
	return []any{
		fieldName, r.Name,
		fieldOwner, r.Owner,
		fieldVisibility, string(r.Visibility),
		fieldDescription, r.Description,
		fieldTemplate, r.Template,
		fieldStatus, string(r.Status),
		fieldURL, r.URL,
		fieldError, r.Error,
		fieldRequestedBy, r.RequestedBy,
		fieldApprover, r.Approver,
		fieldRejectReason, r.RejectReason,
		fieldCreatedAt, r.CreatedAt.Format(time.RFC3339Nano),
		fieldUpdatedAt, r.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func decodeRequest(id string, vals map[string]string) (Request, error) {
	r := Request{
		ID:           id,
		Name:         vals[fieldName],
		Owner:        vals[fieldOwner],
		Visibility:   reporequest.Visibility(vals[fieldVisibility]),
		Description:  vals[fieldDescription],
		Template:     vals[fieldTemplate],
		Status:       reporequest.Status(vals[fieldStatus]),
		URL:          vals[fieldURL],
		Error:        vals[fieldError],
		RequestedBy:  vals[fieldRequestedBy],
		Approver:     vals[fieldApprover],
		RejectReason: vals[fieldRejectReason],
	}
	if raw := vals[fieldCreatedAt]; raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Request{}, fmt.Errorf("decode created_at for %q: %w", id, err)
		}
		r.CreatedAt = t
	}
	if raw := vals[fieldUpdatedAt]; raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Request{}, fmt.Errorf("decode updated_at for %q: %w", id, err)
		}
		r.UpdatedAt = t
	}
	return r, nil
}
