package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

const destructiveMoveTimeout = 10 * time.Minute

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

	if h.Tombstones == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "tombstone store not configured")
		return
	}

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), destructiveMoveTimeout)
	defer cancel()
	lockErr := h.withSiteLock(opCtx, h.DeployPrefix.SiteDirname(site), func() error {
		for _, mode := range []string{"production", "preview"} {
			cur, err := h.R2.GetAlias(opCtx, h.aliasKey(site, mode))
			if err != nil && !r2.IsNotFound(err) {
				writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.delete", err)
				return nil
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
				return nil
			}
		}

		moved, err := h.R2.MovePrefix(opCtx, h.deployPrefix(site, deployID), h.trashPrefix(site, deployID))
		if err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.tombstone", err)
			return nil
		}
		if err := h.Tombstones.RecordTombstone(opCtx, h.DeployPrefix.SiteDirname(site), deployID, 0); err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "tombstone_record_failed", "pg.tombstone.record", err)
			return nil
		}

		slog.Info("site.deploy.tombstoned", "site", site, "deployId", deployID, "moved", moved,
			"reqID", RequestIDFromContext(r.Context()))
		writeJSON(w, http.StatusOK, map[string]any{
			"site":     site,
			"deployId": deployID,
			"status":   "tombstoned",
			"moved":    moved,
		})
		return nil
	})
	if lockErr != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "site_lock_failed", "pg.lock.site", lockErr)
	}
}

func (h *Handlers) trashPrefix(site, id string) string {
	base := h.TrashPrefixBase
	if base == "" {
		base = "_trash/"
	}
	return base + h.DeployPrefix.SiteDirname(site) + "/" + id + "/"
}
