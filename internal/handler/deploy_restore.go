package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/go-chi/chi/v5"
)

const defaultTrashRecovery = 7 * 24 * time.Hour

func (h *Handlers) SiteDeployRestore(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return
	}
	deployID := chi.URLParam(r, "deployId")
	if !deployIDPattern.MatchString(deployID) {
		writeError(w, http.StatusBadRequest, "bad_request", "deployId is not a valid artemis deploy id")
		return
	}

	if h.Trash == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "trash store not configured")
		return
	}

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), destructiveMoveTimeout)
	defer cancel()
	lockErr := h.withSiteLock(opCtx, h.DeployPrefix.SiteDirname(site), func() error {
		if _, err := h.Registry.GetSite(opCtx, site); err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				writeError(w, http.StatusGone, "site_gone", "site was deleted; deploy cannot be restored")
				return nil
			}
			writeUpstreamError(w, r, http.StatusBadGateway, "registry_read_failed", "registry.get.restore", err)
			return nil
		}

		dst := h.deployPrefix(site, deployID)
		moved, err := h.R2.MovePrefix(opCtx, h.trashPrefix(site, deployID), dst)
		if err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.restore", err)
			return nil
		}

		liveBytes, bytesErr := h.R2.PrefixBytes(opCtx, dst)
		if bytesErr != nil {
			slog.Warn("deploy.restore.bytes_unavailable", "site", site, "deployId", deployID,
				"err", bytesErr, "reqID", RequestIDFromContext(r.Context()))
			reportUpstream(r, "bytes_unavailable", "r2.list.bytes.restore", bytesErr)
			liveBytes = 0
		}

		restoreErr := h.Trash.RestoreDeploy(opCtx, h.DeployPrefix.SiteDirname(site), deployID, h.Now().UTC(), liveBytes)
		if restoreErr != nil {
			if !errors.Is(restoreErr, registry.ErrNotFound) {
				writeUpstreamError(w, r, http.StatusBadGateway, "restore_failed", "pg.restore.deploy", restoreErr)
				return nil
			}
			live, hasErr := h.R2.HasPrefix(opCtx, dst)
			if hasErr != nil {
				writeUpstreamError(w, r, http.StatusBadGateway, "r2_has_prefix_failed", "r2.has.restore", hasErr)
				return nil
			}
			if !live {
				writeError(w, http.StatusGone, "already_purged", "tombstone is gone; deploy was already hard-purged")
				return nil
			}
			telemetry.FromContext(r.Context()).SetResource(site, deployID)
			h.logAction(r.Context(), "site.deploy.restore", "idempotent", slog.Int("moved", moved))
			h.auditFromScope(r.Context(), "site.deploy.restore", "idempotent", map[string]any{"moved": moved})
			writeJSON(w, http.StatusOK, map[string]any{
				"site":     site,
				"deployId": deployID,
				"status":   "restored",
				"moved":    moved,
				"bytes":    liveBytes,
			})
			return nil
		}

		telemetry.FromContext(r.Context()).SetResource(site, deployID)
		h.logAction(r.Context(), "site.deploy.restore", "success", slog.Int("moved", moved), slog.Int64("bytes", liveBytes))
		h.auditFromScope(r.Context(), "site.deploy.restore", "success", map[string]any{"moved": moved, "bytes": liveBytes})
		writeJSON(w, http.StatusOK, map[string]any{
			"site":     site,
			"deployId": deployID,
			"status":   "restored",
			"moved":    moved,
			"bytes":    liveBytes,
		})
		return nil
	})
	if lockErr != nil {
		writeLockError(w, r, lockErr)
	}
}

func (h *Handlers) SiteTrashList(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return
	}

	if h.Trash == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "trash store not configured")
		return
	}

	tombstones, err := h.Trash.TombstonesForSite(r.Context(), h.DeployPrefix.SiteDirname(site))
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "pg_read_failed", "pg.tombstones.list", err)
		return
	}

	recovery := h.TrashRecovery
	if recovery <= 0 {
		recovery = defaultTrashRecovery
	}
	out := make([]map[string]any, 0, len(tombstones))
	for _, t := range tombstones {
		out = append(out, map[string]any{
			"deployId":  t.ID,
			"trashedAt": t.TrashedAt.UTC().Format(time.RFC3339),
			"expiresAt": t.TrashedAt.Add(recovery).UTC().Format(time.RFC3339),
			"bytes":     t.Bytes,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
