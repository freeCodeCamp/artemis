// Package registry defines the abstract site-registry contract that
// artemis handlers consume. It deliberately sits above the concrete
// backends (sites.yaml, valkey) so that the rest of the codebase
// depends on the smallest possible surface — one method, one type.
//
// Two implementations exist:
//
//   - internal/registry/sitesyaml — reads from a hot-reloaded YAML file
//     (the legacy helm-embedded ConfigMap path; backward-compat window).
//   - internal/registry/valkey    — reads from the Valkey store
//     (target path, post-cassiopeia GA).
//
// Selection happens in cmd/artemis/main.go based on the
// REGISTRY_BACKEND env var (cf. config.RegistryConfig).
package registry

// Snapshot is a point-in-time view of the registry. Each call to
// Reader.Snapshot returns a freshly captured Snapshot — callers
// holding the returned value see a stable view across multiple
// lookups even if a concurrent writer mutates the underlying store.
type Snapshot interface {
	// Sites returns the registered slugs in stable (typically sorted)
	// order. The returned slice is safe to mutate; callers do not
	// need to copy defensively before iteration.
	Sites() []string

	// TeamsForSite returns the GitHub team slugs authorized for the
	// given site, or nil when the site is not in the registry. The
	// returned slice is safe to mutate.
	TeamsForSite(slug string) []string
}

// Reader is the read-side handler-facing contract. Writers use the
// concrete backend type directly (e.g. *valkey.Store) since
// state-mutating operations require backend-specific atomicity
// primitives (MULTI/EXEC, file rename) that do not generalise.
type Reader interface {
	Snapshot() Snapshot
}
