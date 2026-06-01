package handler

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// readyZProbePrefix is intentionally non-matching: HasPrefix on a key
// that cannot exist still exercises the R2 round-trip without pulling
// real listings, and returns (false, nil) when the bucket is reachable.
const readyZProbePrefix = "_artemis_readyz_probe_no_match"

const readyZProbeTimeout = 5 * time.Second

// ReadyZ implements GET /readyz. Returns {"ready":true} when both
// Valkey and R2 are reachable, otherwise 503 with a code naming the
// failing upstream. No auth, no cache; intended for k8s readiness
// probes.
func (h *Handlers) ReadyZ(w http.ResponseWriter, r *http.Request) {
	var wg sync.WaitGroup
	var valkeyErr, r2Err error

	if h.Health != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), readyZProbeTimeout)
			defer cancel()
			valkeyErr = h.Health.Ping(ctx)
		}()
	}

	if h.R2 != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), readyZProbeTimeout)
			defer cancel()
			_, r2Err = h.R2.HasPrefix(ctx, readyZProbePrefix)
		}()
	}

	wg.Wait()

	switch {
	case valkeyErr != nil:
		writeUpstreamError(w, r, http.StatusServiceUnavailable, "valkey_unreachable", "valkey.ping", valkeyErr)
	case r2Err != nil:
		writeUpstreamError(w, r, http.StatusServiceUnavailable, "r2_unreachable", "r2.has_prefix", r2Err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
	}
}
