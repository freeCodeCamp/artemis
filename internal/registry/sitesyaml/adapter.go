// Package sitesyaml adapts the legacy sites.yaml-backed loader to the
// registry.Reader interface. It exists for the one-release backward-
// compat window during the Valkey migration; once operators flip to
// REGISTRY_BACKEND=valkey and the soak window passes, this package
// retires alongside internal/sites.
package sitesyaml

import (
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sites"
)

// Adapter wraps *sites.Loader to satisfy registry.Reader. No new
// state — every call passes through to the underlying loader.
type Adapter struct {
	Loader *sites.Loader
}

// New returns an Adapter wrapping the given loader. The loader's
// hot-reload goroutine (Watch) must be started by the caller; the
// adapter does not own its lifecycle.
func New(l *sites.Loader) *Adapter {
	return &Adapter{Loader: l}
}

// Snapshot returns the loader's latest valid snapshot, typed as the
// registry.Snapshot interface. sites.Snapshot satisfies the interface
// directly (Sites + TeamsForSite methods) so no copy or conversion
// is needed.
func (a *Adapter) Snapshot() registry.Snapshot {
	return a.Loader.Snapshot()
}
