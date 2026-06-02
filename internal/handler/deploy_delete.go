package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) SiteDeployDelete(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return
	}
	deployID := chi.URLParam(r, "deployId")
	if !deployIDPattern.MatchString(deployID) {
		writeError(w, http.StatusBadRequest, "bad_request", "deployId is not a valid artemis deploy id")
		return
	}

	for _, mode := range []string{"production", "preview"} {
		cur, err := h.R2.GetAlias(r.Context(), h.aliasKey(site, mode))
		if err != nil && !r2.IsNotFound(err) {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.delete", err)
			return
		}
		if strings.TrimSpace(cur) == deployID {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{
					"code":    "deploy_aliased",
					"message": "deploy is the target of a live alias; promote or roll back before deleting",
				},
				"site":     site,
				"deployId": deployID,
				"alias":    mode,
			})
			return
		}
	}

	if h.Tombstones == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "tombstone store not configured")
		return
	}

	moved, err := h.R2.MovePrefix(r.Context(), h.deployPrefix(site, deployID), h.trashPrefix(site, deployID))
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.tombstone", err)
		return
	}
	if err := h.Tombstones.RecordTombstone(r.Context(), site, deployID, 0); err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "tombstone_record_failed", "pg.tombstone.record", err)
		return
	}

	slog.Info("site.deploy.tombstoned", "site", site, "deployId", deployID, "moved", moved,
		"reqID", RequestIDFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"site":     site,
		"deployId": deployID,
		"status":   "tombstoned",
		"moved":    moved,
	})
}

func (h *Handlers) trashPrefix(site, id string) string {
	base := h.TrashPrefixBase
	if base == "" {
		base = "_trash/"
	}
	return base + site + "/" + id + "/"
}
