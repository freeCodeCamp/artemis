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
	var (
		moved     int
		liveBytes int64
		outcome   string
	)
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
		var err error
		moved, err = h.R2.MovePrefix(opCtx, h.trashPrefix(site, deployID), dst)
		if err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.restore", err)
			return nil
		}

		var bytesErr error
		liveBytes, bytesErr = h.R2.PrefixBytes(opCtx, dst)
		if bytesErr != nil {
			slog.WarnContext(r.Context(), "deploy.restore.bytes_unavailable", "site", site, "deploy_id", deployID,
				"err", bytesErr)
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
			outcome = "idempotent"
			return nil
		}

		outcome = "success"
		return nil
	})
	if lockErr != nil {
		writeLockError(w, r, lockErr)
		return
	}
	if outcome == "" {
		return
	}

	telemetry.FromContext(r.Context()).SetResource(site, deployID)
	attrs := []slog.Attr{slog.Int("moved", moved)}
	detail := map[string]any{"moved": moved}
	if outcome == "success" {
		attrs = append(attrs, slog.Int64("bytes", liveBytes))
		detail["bytes"] = liveBytes
	}
	h.logAction(r.Context(), "site.deploy.restore", outcome, attrs...)
	h.auditFromScope(r.Context(), "site.deploy.restore", outcome, detail)
	writeJSON(w, http.StatusOK, map[string]any{
		"site":     site,
		"deployId": deployID,
		"status":   "restored",
		"moved":    moved,
		"bytes":    liveBytes,
	})
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
