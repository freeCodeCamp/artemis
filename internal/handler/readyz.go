package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

const readyZProbeTimeout = 5 * time.Second

const readyzPageThreshold = 3

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
			r2Err = h.R2.Ping(ctx)
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

	if r.Context().Err() != nil {
		return
	}

	var degraded bool

	if valkeyErr != nil {
		degraded = true
		if h.readyzValkey.observe(true, true) {
			captureProbeFailure(r, "valkey_unreachable", "valkey.ping", valkeyErr)
		}
		slog.WarnContext(r.Context(), "readyz.valkey.degraded", "err", valkeyErr)
	} else {
		h.readyzValkey.observe(false, false)
	}

	if r2Err != nil {
		degraded = true
		if h.readyzR2.observe(true, true) {
			captureProbeFailure(r, "r2_unreachable", "r2.ping", r2Err)
		}
		slog.WarnContext(r.Context(), "readyz.r2.degraded", "err", r2Err)
	} else {
		h.readyzR2.observe(false, false)
	}

	if pgErr != nil {
		degraded = true
		slog.WarnContext(r.Context(), "readyz.postgres.degraded", "err", pgErr)
	}

	if degraded {
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true, "degraded": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
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

func captureProbeFailure(r *http.Request, code, op string, err error) {
	hub := sentry.GetHubFromContext(r.Context())
	if hub == nil {
		return
	}
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("op", op)
		scope.SetTag("error_code", code)
		scope.SetFingerprint([]string{"readyz", op})
		hub.CaptureException(err)
	})
}
