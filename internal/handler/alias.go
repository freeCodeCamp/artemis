package handler

import (
	"net/http"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

// AliasGet implements GET /api/site/{site}/alias/{mode} — read-only
// access to the deploy id currently pointed at by the preview or
// production alias. Used to unblock the pre-promote echo workflow and
// by the client-side CAS workflow that compares the current alias body
// before issuing a promote/rollback.
//
// Authz mirrors the other /api/site/{site}/* read-paths: caller must
// be on at least one of the site's authorized teams. Returns 400 on
// unknown mode, 404 when the alias key has not been written yet
// (fresh site, never finalized), 502 on R2 transport errors.
func (h *Handlers) AliasGet(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	mode := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "mode")))

	switch mode {
	case "preview", "production":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "mode must be preview or production")
		return
	}

	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return // already wrote response
	}

	deployID, err := h.R2.GetAlias(r.Context(), h.aliasKey(site, mode))
	if err != nil {
		if r2.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "no_alias", "alias has not been set for this site/mode")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias", err)
		return
	}
	deployID = strings.TrimSpace(deployID)
	if deployID == "" {
		writeError(w, http.StatusNotFound, "no_alias", "alias body is empty")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"site":     site,
		"mode":     mode,
		"deployId": deployID,
		"url":      h.publicURL(site, mode),
	})
}
