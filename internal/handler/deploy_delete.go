package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
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
	var (
		moved   int
		success bool
	)
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

		deployBytes, bytesErr := h.R2.PrefixBytes(opCtx, h.deployPrefix(site, deployID))
		if bytesErr != nil {
			deployBytes = 0
		}
		var err error
		moved, err = h.R2.MovePrefix(opCtx, h.deployPrefix(site, deployID), h.trashPrefix(site, deployID))
		if err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.tombstone", err)
			return nil
		}
		if err := h.Tombstones.RecordTombstone(opCtx, h.DeployPrefix.SiteDirname(site), deployID, deployBytes); err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "tombstone_record_failed", "pg.tombstone.record", err)
			return nil
		}

		success = true
		return nil
	})
	if lockErr != nil {
		writeLockError(w, r, lockErr)
		return
	}
	if !success {
		return
	}

	telemetry.FromContext(r.Context()).SetResource(site, deployID)
	h.logAction(r.Context(), "site.deploy.delete", "success", slog.Int("moved", moved))
	h.auditFromScope(r.Context(), "site.deploy.delete", "success", map[string]any{"moved": moved})
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
	return base + h.DeployPrefix.SiteDirname(site) + "/" + id + "/"
}
