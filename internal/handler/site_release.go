package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
)

// SiteRelease ends a reservation early per ADR 0006 step 7b. Authz is
// RepoApproveAuthzTeam, not RegistryAuthzTeam: delete is reversible and
// release is not, so the two must not share a gate.
func (h *Handlers) SiteRelease(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRepoTeam(w, r, h.RepoApproveAuthzTeam); err != nil {
		return
	}
	slug := sitekey.Slug(chi.URLParam(r, "slug"))
	if !slugRe.MatchString(string(slug)) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}
	if h.NameReleaser == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "reservation store not configured")
		return
	}

	dirname := h.DeployPrefix.SiteDirname(slug)
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), destructiveMoveTimeout)
	defer cancel()

	auditReleaseFailure := func(stage string) {
		telemetry.FromContext(r.Context()).SetResource(string(slug), "")
		h.auditFromScope(r.Context(), "site.release", "failure", map[string]any{"stage": stage})
	}

	var (
		moved int
		wrote bool
	)
	lockErr := h.withSiteLock(opCtx, dirname, func(opCtx context.Context) error {
		_, err := h.requireWritableSite(opCtx, slug)
		if !errors.Is(err, registry.ErrReserved) {
			auditReleaseFailure("state")
			if err == nil || errors.Is(err, registry.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "site is not a reserved name")
			} else {
				writeUpstreamError(w, r, http.StatusBadGateway, "registry_read_failed", "pg.get.site", err)
			}
			wrote = true
			return nil
		}
		if err := h.NameReleaser.ExpireReservation(opCtx, slug); err != nil {
			auditReleaseFailure("disarm")
			writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "pg.expire.reservation", err)
			wrote = true
			return nil
		}
		if h.Tombstones != nil {
			if err := h.Tombstones.RecordSiteTombstone(opCtx, dirname); err != nil {
				auditReleaseFailure("tombstone")
				writeUpstreamError(w, r, http.StatusBadGateway, "tombstone_record_failed", "pg.tombstone.site-release", err)
				wrote = true
				return nil
			}
		}
		base := h.TrashPrefixBase
		if base == "" {
			base = "_trash/"
		}
		src := string(dirname) + "/"
		n, err := h.R2.MovePrefix(opCtx, src, base+src)
		moved = n
		if err != nil {
			auditReleaseFailure("reclaim")
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.site-release", err)
			wrote = true
			return nil
		}
		// COMPATIBILITY entry 19: bytes first, name second. A name freed
		// while its objects are at the origin lets a new owner inherit
		// a stranger's site, and SiteRegister takes no site lock.
		if err := h.NameReleaser.ReleaseReservationNow(opCtx, slug); err != nil {
			auditReleaseFailure("release")
			if errors.Is(err, registry.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "site is not a reserved name")
			} else {
				writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "pg.release.reservation", err)
			}
			wrote = true
			return nil
		}
		return nil
	})
	if wrote {
		return
	}
	if lockErr != nil {
		writeLockError(w, r, lockErr)
		return
	}

	telemetry.FromContext(r.Context()).SetResource(string(slug), "")
	h.logAction(r.Context(), "site.release", "success", slog.Int("moved", moved))
	h.auditFromScope(r.Context(), "site.release", "success", map[string]any{"moved": moved})
	writeJSON(w, http.StatusOK, map[string]any{
		"slug": string(slug), "status": "released", "moved": moved,
	})
}
