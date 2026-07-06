package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

// readyZProbePrefix is intentionally non-matching: HasPrefix on a key
// that cannot exist still exercises the R2 round-trip without pulling
// real listings, and returns (false, nil) when the bucket is reachable.
const readyZProbePrefix = "_artemis_readyz_probe_no_match"

const readyZProbeTimeout = 5 * time.Second

const readyzPageThreshold = 3

// ReadyZ implements GET /readyz. Returns {"ready":true} when both
// Valkey and R2 are reachable, otherwise 503 with a code naming the
// failing upstream. No auth, no cache; intended for k8s readiness
// probes.
func (h *Handlers) ReadyZ(w http.ResponseWriter, r *http.Request) {
	var wg sync.WaitGroup
	var valkeyErr, r2Err, pgErr error

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

	if h.PGHealth != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), readyZProbeTimeout)
			defer cancel()
			pgErr = h.PGHealth.Ping(ctx)
		}()
	}

	wg.Wait()

	switch {
	case valkeyErr != nil:
		page := h.readyzValkey.observe(true, true)
		h.readyzR2.observe(r2Err != nil, false)
		writeProbeUnavailable(w, r, "valkey_unreachable", "valkey.ping", valkeyErr, page)
	case r2Err != nil:
		h.readyzValkey.observe(false, false)
		page := h.readyzR2.observe(true, true)
		writeProbeUnavailable(w, r, "r2_unreachable", "r2.has_prefix", r2Err, page)
	case pgErr != nil:
		h.readyzValkey.observe(false, false)
		h.readyzR2.observe(false, false)
		slog.ErrorContext(r.Context(), "readyz.postgres.degraded", "err", pgErr)
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true, "degraded": true})
	default:
		h.readyzValkey.observe(false, false)
		h.readyzR2.observe(false, false)
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
	}
}

type probeState struct {
	mu    sync.Mutex
	fails int
	paged bool
}

func (p *probeState) observe(failed, report bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !failed {
		p.fails = 0
		p.paged = false
		return false
	}
	p.fails++
	if report && p.fails >= readyzPageThreshold && !p.paged {
		p.paged = true
		return true
	}
	return false
}

func writeProbeUnavailable(w http.ResponseWriter, r *http.Request, code, op string, err error, page bool) {
	slog.WarnContext(r.Context(), "readyz.probe.unavailable",
		"op", op,
		"err", err,
	)
	if pkgMetrics != nil {
		pkgMetrics.UpstreamErrors.WithLabelValues(op).Inc()
	}
	if page {
		if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("op", op)
				scope.SetTag("error_code", code)
				scope.SetFingerprint([]string{"readyz", op})
				hub.CaptureException(err)
			})
		}
	}
	writeError(w, http.StatusServiceUnavailable, code, "upstream call failed")
}
