package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

// SiteRow is the canonical JSON shape for a registry row across
// register / list / update endpoints. The shape is stable so
// universe-cli can decode the same struct from any of them.
type SiteRow struct {
	Slug      string    `json:"slug"`
	Teams     []string  `json:"teams"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedBy string    `json:"createdBy"`
}

func toSiteRow(s registry.Site) SiteRow {
	return SiteRow{
		Slug:      s.Slug,
		Teams:     s.Teams,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		CreatedBy: s.CreatedBy,
	}
}

// SiteRegisterRequest is the body of POST /api/site/register.
type SiteRegisterRequest struct {
	Slug  string   `json:"slug"`
	Teams []string `json:"teams,omitempty"`
}

// SiteRegisterResponse is the 201 body returned on successful
// registration. Alias of SiteRow so the on-the-wire shape across
// register / list / update endpoints is stable.
type SiteRegisterResponse = SiteRow

// slugRe matches DNS-safe site slugs: 1-63 chars, lowercase letter
// first, then lowercase letters / digits / hyphens. Mirrors the
// `<site>.freecode.camp` constraint — slugs become subdomains.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// teamSlugRe matches GitHub team slugs: 1-39 chars, lowercase letter
// or digit first, then lowercase letters / digits / hyphens / underscores.
var teamSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,38}$`)

// SiteRegister implements POST /api/site/register — creates a new
// site row in the registry. Authz: caller must be on
// h.RegistryAuthzTeam (default "staff"). On empty/missing teams field
// the handler defaults to [h.RegistryAuthzTeam].
//
// Status matrix:
//
//	201 Created         — registered; body = SiteRegisterResponse
//	400 Bad Request     — invalid slug / team format / json
//	403 Forbidden       — caller not in the authz team
//	409 Conflict        — slug already registered
//	502 Bad Gateway     — registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteRegister(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}

	var req SiteRegisterRequest
	if !decodeJSON(w, r, &req, maxJSONBodyBytes) {
		return
	}
	if !slugRe.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}

	teams := req.Teams
	if len(teams) == 0 {
		teams = []string{h.RegistryAuthzTeam}
	}
	for _, t := range teams {
		if !teamSlugRe.MatchString(t) {
			writeError(w, http.StatusBadRequest, "invalid_team",
				"team slugs must be 1-39 chars matching [a-z0-9][a-z0-9_-]*")
			return
		}
	}

	login := LoginFromContext(r.Context())
	site, err := h.Registry.Register(r.Context(), req.Slug, teams, login)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "already_exists", "site is already registered")
		default:
			writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "valkey.register", err)
		}
		return
	}

	slog.Info("site.register", "slug", req.Slug, "teams", teams, "by", login, "reqID", RequestIDFromContext(r.Context()))
	writeJSON(w, http.StatusCreated, toSiteRow(site))
}

// SiteUpdateRequest is the body of PATCH /api/site/{slug}.
type SiteUpdateRequest struct {
	Teams []string `json:"teams"`
}

// SiteUpdate implements PATCH /api/site/{slug} — replaces the teams
// list for an existing site. Authz: caller in h.RegistryAuthzTeam.
//
// Status matrix:
//
//	200 OK             — body = SiteRow
//	400 Bad Request    — invalid teams / json
//	403 Forbidden      — caller not in authz team
//	404 Not Found      — slug not registered
//	502 Bad Gateway    — registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}
	slug := chi.URLParam(r, "slug")
	if !slugRe.MatchString(slug) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}

	var req SiteUpdateRequest
	if !decodeJSON(w, r, &req, maxJSONBodyBytes) {
		return
	}
	if len(req.Teams) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_team",
			"teams must contain at least one slug; use DELETE to remove a site")
		return
	}
	for _, t := range req.Teams {
		if !teamSlugRe.MatchString(t) {
			writeError(w, http.StatusBadRequest, "invalid_team",
				"team slugs must be 1-39 chars matching [a-z0-9][a-z0-9_-]*")
			return
		}
	}

	site, err := h.Registry.UpdateTeams(r.Context(), slug, req.Teams)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "site is not registered")
		default:
			writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "valkey.update", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, toSiteRow(site))
}

// SiteDelete implements DELETE /api/site/{slug} — removes a slug
// from the registry. R2 deploy bytes are NOT touched (those age out
// via the post-GA cleanup cron). Authz: caller in
// h.RegistryAuthzTeam.
//
// Status matrix:
//
//	204 No Content     — deleted
//	400 Bad Request    — invalid slug
//	403 Forbidden      — caller not in authz team
//	404 Not Found      — slug not registered
//	502 Bad Gateway    — registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}
	slug := chi.URLParam(r, "slug")
	if !slugRe.MatchString(slug) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}

	if r.URL.Query().Get("purge") != "true" {
		if err := h.Registry.Delete(r.Context(), slug); err != nil {
			writeRegistryDeleteError(w, r, err)
			return
		}
		slog.Info("site.delete", "slug", slug, "reqID", RequestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if h.Tombstones == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "tombstone store not configured")
		return
	}
	base := h.TrashPrefixBase
	if base == "" {
		base = "_trash/"
	}
	dirname := h.DeployPrefix.SiteDirname(slug)
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), destructiveMoveTimeout)
	defer cancel()
	lockErr := h.withSiteLock(opCtx, dirname, func() error {
		moved, err := h.R2.MovePrefix(opCtx, dirname+"/", base+dirname+"/")
		if err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_move_failed", "r2.move.site-purge", err)
			return nil
		}
		if err := h.Tombstones.RecordTombstone(opCtx, dirname, "", 0); err != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "tombstone_record_failed", "pg.tombstone.site-purge", err)
			return nil
		}
		if err := h.Registry.Delete(opCtx, slug); err != nil {
			writeRegistryDeleteError(w, r, err)
			return nil
		}
		slog.Info("site.purge", "slug", slug, "moved", moved, "reqID", RequestIDFromContext(r.Context()))
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "status": "purged", "moved": moved})
		return nil
	})
	if lockErr != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "site_lock_failed", "pg.lock.site", lockErr)
	}
}

func writeRegistryDeleteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, registry.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "site is not registered")
		return
	}
	writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "valkey.delete", err)
}

// SitesList implements GET /api/sites — enumerates every registered
// site row. Open to any GH bearer (no special authz beyond
// authentication). Reads the source of truth on every request — no
// in-process cache here; staleness <60s is bounded by the registry
// reader's TTL fallback for the deploy hot path, but list/dashboard
// callers want the freshest view.
//
// Status matrix:
//
//	200 OK             — body = []SiteRow
//	502 Bad Gateway    — registry read failed
func (h *Handlers) SitesList(w http.ResponseWriter, r *http.Request) {
	sites, err := h.Registry.Sites(r.Context())
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "registry_read_failed", "valkey.list", err)
		return
	}
	rows := make([]SiteRow, len(sites))
	for i, s := range sites {
		rows[i] = toSiteRow(s)
	}
	writeJSON(w, http.StatusOK, rows)
}

// requireRegistryAuthz enforces that the authenticated caller is on
// h.RegistryAuthzTeam. Writes the response on failure and returns a
// non-nil error so the caller can early-return.
func (h *Handlers) requireRegistryAuthz(w http.ResponseWriter, r *http.Request) error {
	if h.RegistryAuthzTeam == "" {
		writeError(w, http.StatusInternalServerError, "misconfigured", "RegistryAuthzTeam not set")
		return errBadRequest
	}
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())
	ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, []string{h.RegistryAuthzTeam})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
		return err
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized",
			"caller is not on the registry-authz team")
		return errBadRequest
	}
	return nil
}
