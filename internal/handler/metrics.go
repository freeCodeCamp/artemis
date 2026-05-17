package handler

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the prometheus collectors instrumented from the
// handler + registry layers. A nil *Metrics value disables
// instrumentation, so unit tests that don't care about counters can
// leave the field unset on Handlers.
type Metrics struct {
	// RegistryRefreshFailures counts every full-table refresh that
	// errored out of the registry/valkey reader's run() loop. Stale
	// snapshot keeps serving; this counter surfaces the failure.
	RegistryRefreshFailures prometheus.Counter

	// AliasDrift counts every 409 alias_drift from SitePromote /
	// SiteRollback. Spikes indicate concurrent writers or out-of-sync
	// clients.
	AliasDrift prometheus.Counter

	// PromoteLegacyBare counts empty-body POST /api/site/{site}/promote
	// requests (no expectedCurrent body-pin). The counter exists to
	// confirm v0.3.0 can safely flip the surface to require a body
	// pin — when this metric is 0 across a release window, the flip
	// is unblocked.
	PromoteLegacyBare prometheus.Counter

	// UpstreamErrors counts every writeUpstreamError invocation,
	// labelled by the `op` tag (e.g. r2.put.alias, valkey.register).
	// Provides a single dashboard surface for upstream-dependency
	// reliability.
	UpstreamErrors *prometheus.CounterVec
}

// pkgMetrics holds the package-level metrics handle so package-private
// helpers (writeUpstreamError, AccessLog) can record events without
// threading *Handlers through every call site. cmd/artemis is expected
// to invoke SetMetrics once at startup. Nil means "no instrumentation"
// — tests that don't touch counters can ignore it.
var pkgMetrics *Metrics

// SetMetrics installs the package-level metrics handle. Safe to call
// once at startup; subsequent calls replace the handle.
func SetMetrics(m *Metrics) { pkgMetrics = m }

// NewMetrics registers the artemis counters with the given registerer
// and returns the collector handle. Use a fresh prometheus.Registry
// in tests to avoid duplicate-registration panics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		RegistryRefreshFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_registry_refresh_failures_total",
			Help: "Count of full-snapshot refreshes from registry/valkey that returned an error; stale snapshot stays served.",
		}),
		AliasDrift: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_alias_drift_total",
			Help: "Count of 409 alias_drift responses from SitePromote + SiteRollback (CAS body-pin mismatch).",
		}),
		PromoteLegacyBare: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "artemis_promote_legacy_bare_total",
			Help: "Count of POST /api/site/{site}/promote with no expectedCurrent body — surfaces clients that have not adopted CAS.",
		}),
		UpstreamErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "artemis_upstream_error_total",
			Help: "Count of writeUpstreamError invocations, labelled by op (r2.put.alias, valkey.register, etc).",
		}, []string{"op"}),
	}
	reg.MustRegister(m.RegistryRefreshFailures, m.AliasDrift, m.PromoteLegacyBare, m.UpstreamErrors)
	return m
}

// MetricsHandler returns the /metrics http.Handler over the supplied
// gatherer. cmd/artemis wires this against the same registry it built
// the Metrics with.
func MetricsHandler(g prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(g, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
