package handler

import "net/http"

// HealthZ implements GET /healthz. Returns {"ok":true} with no auth, no
// upstream calls. Used by k8s liveness/readiness probes.
func (h *Handlers) HealthZ(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
