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

// Writer is the state-mutating contract for the site registry. Each
// method is required to be atomic at the source-of-truth layer
// (e.g. Valkey MULTI/EXEC) so concurrent callers never observe a
// partial state.
type Writer interface {
	// Register creates a new site row and publishes a
	// registry.changed event on success. Returns ErrAlreadyExists
	// when slug is already registered.
	Register(ctx context.Context, slug string, teams []string, createdBy string) (Site, error)
}
