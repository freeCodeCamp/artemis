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

	h.readyzValkey.observe(valkeyErr != nil)
	h.readyzR2.observe(r2Err != nil)

	switch {
	case valkeyErr != nil:
		writeProbeUnavailable(w, r, "valkey_unreachable", "valkey.ping", valkeyErr, h.readyzValkey.takePage())
	case r2Err != nil:
		writeProbeUnavailable(w, r, "r2_unreachable", "r2.has_prefix", r2Err, h.readyzR2.takePage())
	case pgErr != nil:
		slog.Error("readyz: postgres degraded", "err", pgErr)
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true, "degraded": true})
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
	}
}

type probeState struct {
	mu    sync.Mutex
	fails int
	paged bool
}

func (p *probeState) observe(failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !failed {
		p.fails = 0
		p.paged = false
		return
	}
	p.fails++
}

func (p *probeState) takePage() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails >= readyzPageThreshold && !p.paged {
		p.paged = true
		return true
	}
	return false
}

func writeProbeUnavailable(w http.ResponseWriter, r *http.Request, code, op string, err error, page bool) {
	slog.Warn("readiness probe upstream unavailable",
		"op", op,
		"err", err,
		"reqID", RequestIDFromContext(r.Context()),
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
