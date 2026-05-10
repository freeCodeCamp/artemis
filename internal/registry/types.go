package registry

import (
	"context"
	"errors"
	"time"
)

// Site is the in-memory representation of one registry row. It is the
// shape that crosses the package boundary between backends and
// handlers; backends are responsible for marshalling to/from their
// wire encodings (e.g. Valkey hash fields).
type Site struct {
	Slug      string
	Teams     []string
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedBy string
}

// Sentinel errors returned by Writer operations. Callers compare with
// errors.Is.
var (
	// ErrAlreadyExists is returned by Register when the slug is
	// already in the registry. The HTTP layer maps this to 409.
	ErrAlreadyExists = errors.New("registry: site already exists")

	// ErrNotFound is returned when an operation targets a slug that
	// is not in the registry. The HTTP layer maps this to 404.
	ErrNotFound = errors.New("registry: site not found")
)

// Writer is the registry contract handlers depend on for both reads
// of the full Site row (timestamps, createdBy) and atomic writes.
// State-mutating methods are required to be atomic at the
// source-of-truth layer (e.g. Valkey MULTI/EXEC) so concurrent
// callers never observe a partial state.
//
// Cache-light reads (just `<slug> -> [teams]`) go through Reader /
// Snapshot instead — those are served from an in-process cache
// refreshed on registry.changed events.
type Writer interface {
	// Sites returns every registered site row, sorted by slug
	// ascending. Reads bypass any in-process cache; the response
	// reflects the source-of-truth at call time.
	Sites(ctx context.Context) ([]Site, error)

	// Register creates a new site row and publishes a
	// registry.changed event on success. Returns ErrAlreadyExists
	// when slug is already registered.
	Register(ctx context.Context, slug string, teams []string, createdBy string) (Site, error)

	// UpdateTeams replaces the teams list for an existing slug,
	// stamps updated_at, and publishes a registry.changed event.
	// Returns ErrNotFound if the slug is absent.
	UpdateTeams(ctx context.Context, slug string, teams []string) (Site, error)

	// Delete removes a slug from the registry (hash row + index set
	// member) and publishes a registry.changed event. Returns
	// ErrNotFound if the slug is absent. The deletion does NOT
	// touch any deploy bytes in R2 — those age out via the
	// post-GA cleanup cron.
	Delete(ctx context.Context, slug string) error
}
