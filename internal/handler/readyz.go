package handler

import (
	"context"
	"net/http"
	"time"
)

// readyZProbePrefix is intentionally non-matching: HasPrefix on a key
// that cannot exist still exercises the R2 round-trip without pulling
// real listings, and returns (false, nil) when the bucket is reachable.
const readyZProbePrefix = "_artemis_readyz_probe_no_match"

// readyZProbeTimeout caps each upstream probe. Two probes (Valkey,
// R2) sequential — total budget = 2 × readyZProbeTimeout.
const readyZProbeTimeout = 3 * time.Second

// ReadyZ implements GET /readyz. Returns {"ready":true} when both
// Valkey and R2 are reachable, otherwise 503 with a code naming the
// failing upstream. No auth, no cache; intended for k8s readiness
// probes.
func (h *Handlers) ReadyZ(w http.ResponseWriter, r *http.Request) {
	if h.Health != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyZProbeTimeout)
		if err := h.Health.Ping(ctx); err != nil {
			cancel()
			writeUpstreamError(w, r, http.StatusServiceUnavailable, "valkey_unreachable", "valkey.ping", err)
			return
		}
		cancel()
	}

	if h.R2 != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyZProbeTimeout)
		if _, err := h.R2.HasPrefix(ctx, readyZProbePrefix); err != nil {
			cancel()
			writeUpstreamError(w, r, http.StatusServiceUnavailable, "r2_unreachable", "r2.has_prefix", err)
			return
		}
		cancel()
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
}
