package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/telemetry"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

// SiteRow is the canonical JSON shape for a registry row across
// register / list / update endpoints. The shape is stable so
// universe-cli can decode the same struct from any of them.
type SiteRow struct {
	Slug          sitekey.Slug `json:"slug"`
	Teams         []string     `json:"teams"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
	CreatedBy     string       `json:"createdBy"`
	State         string       `json:"state"`
	ReservedUntil *time.Time   `json:"reservedUntil,omitempty"`
}

func toSiteRow(s registry.Site) SiteRow {
	state := s.State
	if state == "" {
		state = registry.StateActive
	}
	row := SiteRow{
		Slug:      s.Slug,
		Teams:     s.Teams,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		CreatedBy: s.CreatedBy,
		State:     state,
	}
	if s.IsReserved() && !s.ReservedUntil.IsZero() {
		until := s.ReservedUntil
		row.ReservedUntil = &until
	}
	return row
}

// SiteRegisterRequest is the body of POST /api/site/register.
type SiteRegisterRequest struct {
	Slug  sitekey.Slug `json:"slug"`
	Teams []string     `json:"teams,omitempty"`
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
	if !slugRe.MatchString(string(req.Slug)) {
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
		case errors.Is(err, registry.ErrReserved):
			writeError(w, http.StatusConflict, "site_reserved",
				"site name is reserved after a delete; undelete it or wait for the reclaim")
		case errors.Is(err, registry.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "already_exists", "site is already registered")
		default:
			writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "valkey.register", err)
		}
		return
	}

	slog.InfoContext(r.Context(), "site.register", "site", req.Slug, "teams", teams)
	telemetry.FromContext(r.Context()).SetResource(string(req.Slug), "")
	h.auditFromScope(r.Context(), "site.register", "success", map[string]any{"teams": teams, "createdBy": login})
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
//	409 Conflict       — slug is reserved (site_reserved)
//	502 Bad Gateway    — registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteUpdate(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}
	slug := sitekey.Slug(chi.URLParam(r, "slug"))
	if !slugRe.MatchString(string(slug)) {
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

	var (
		before    registry.Site
		beforeErr error
		site      registry.Site
		wrote     bool
	)
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)
	defer cancel()
	lockErr := h.withSiteLock(opCtx, h.DeployPrefix.SiteDirname(slug), func(opCtx context.Context) error {
		before, beforeErr = h.requireWritableSite(opCtx, slug)
		if errors.Is(beforeErr, registry.ErrReserved) {
			h.writeFenceError(w, r, "registry.get.update", "site is not registered", before, beforeErr)
			wrote = true
			return nil
		}
		var err error
		site, err = h.Registry.UpdateTeams(opCtx, slug, req.Teams)
		if err != nil {
			switch {
			case errors.Is(err, registry.ErrNotFound):
				writeError(w, http.StatusNotFound, "not_found", "site is not registered")
			default:
				writeUpstreamError(w, r, http.StatusBadGateway, "registry_write_failed", "valkey.update", err)
			}
			wrote = true
		}
		return nil
	})
	if lockErr != nil {
		writeLockError(w, r, lockErr)
		return
	}
	if wrote {
		return
	}
	beforeTeams := any(before.Teams)
	if beforeErr != nil {
		beforeTeams = "unknown"
	}
	telemetry.FromContext(r.Context()).SetResource(string(slug), "")
	h.logAction(r.Context(), "site.update", "success",
		slog.Any("before", beforeTeams), slog.Any("after", site.Teams))
	h.auditFromScope(r.Context(), "site.update", "success", map[string]any{"before": beforeTeams, "after": site.Teams})
	row := toSiteRow(site)
	if !h.callerSeesActors(r) {
		row.CreatedBy = ""
	}
	writeJSON(w, http.StatusOK, row)
}

// SiteDelete implements DELETE /api/site/{slug} per ADR 0006: remove both
// R2 alias objects, then reserve the name for h.ReservationGrace. Authz:
// caller in h.RegistryAuthzTeam. ?purge is retired and now refused, so no
// caller can mistake a reserving delete for a byte reclaim; POST
// /api/site/{slug}/release performs the reclaim, gated on the approver team.
//
// Status matrix:
//
//	200 OK             — an orphaned alias was unpublished; no row to reserve
//	204 No Content     — unpublished, name reserved
//	400 Bad Request    — invalid slug, or the retired ?purge flag
//	403 Forbidden      — caller not in authz team
//	404 Not Found      — slug not registered and no alias served it
//	502 Bad Gateway    — R2 alias delete or registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}
	slug := sitekey.Slug(chi.URLParam(r, "slug"))
	if !slugRe.MatchString(string(slug)) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}
	if requestsPurge(r) {
		writeError(w, http.StatusBadRequest, "purge_retired",
			"?purge is retired: DELETE unpublishes and holds the name for its grace. To reclaim the bytes and free the name now, call POST /api/site/{slug}/release")
		return
	}
	h.siteDeleteReserving(w, r, slug)
}

func requestsPurge(r *http.Request) bool {
	values, ok := r.URL.Query()["purge"]
	if !ok {
		return false
	}
	for _, v := range values {
		purge, err := strconv.ParseBool(v)
		if err != nil || purge {
			return true
		}
	}
	return false
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
//	200 OK             — body = []SiteRow (active sites; ?state=reserved lists held names)
//	502 Bad Gateway    — registry read failed
func (h *Handlers) SitesList(w http.ResponseWriter, r *http.Request) {
	sites, err := h.Registry.Sites(r.Context())
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "registry_read_failed", "valkey.list", err)
		return
	}
	seesActors := h.callerSeesActors(r)
	state := r.URL.Query().Get("state")
	if state != "" && state != registry.StateActive && state != registry.StateReserved {
		writeError(w, http.StatusBadRequest, "invalid_state", "state must be active or reserved")
		return
	}
	wantReserved := state == registry.StateReserved
	rows := make([]SiteRow, 0, len(sites))
	for _, s := range sites {
		if s.IsReserved() != wantReserved {
			continue
		}
		row := toSiteRow(s)
		if !seesActors {
			row.CreatedBy = ""
		}
		rows = append(rows, row)
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
		writeGitHubProbeError(w, err)
		return err
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized",
			"caller is not on the registry-authz team")
		return errBadRequest
	}
	return nil
}

func (h *Handlers) siteDeleteReserving(w http.ResponseWriter, r *http.Request, slug sitekey.Slug) {
	if h.Reservations == nil {
		if err := h.Registry.Delete(r.Context(), slug); err != nil {
			writeRegistryDeleteError(w, r, err)
			return
		}
		telemetry.FromContext(r.Context()).SetResource(string(slug), "")
		h.logAction(r.Context(), "site.delete", "success")
		h.auditFromScope(r.Context(), "site.delete", "success", nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	dirname := h.DeployPrefix.SiteDirname(slug)
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)
	defer cancel()

	auditDeleteFailure := func(stage string) {
		telemetry.FromContext(r.Context()).SetResource(string(slug), "")
		h.auditFromScope(r.Context(), "site.delete", "failure", map[string]any{"stage": stage})
	}

	var wrote, servedOrphan, orphanObserved, probeUnreadable bool
	lockErr := h.withSiteLock(opCtx, dirname, func(opCtx context.Context) error {
		var served bool
		var headErr error
		var observed registry.ObservedAliases
		modes := []string{"production", "preview"}
		for _, mode := range modes {
			cur, err := h.R2.GetAlias(opCtx, h.aliasKey(slug, mode))
			if err != nil && !r2.IsNotFound(err) {
				headErr = err
				continue
			}
			if r2.IsNotFound(err) {
				cur = ""
			}
			cur = strings.TrimSpace(cur)
			switch mode {
			case "production":
				observed.Production = &cur
			case "preview":
				observed.Preview = &cur
			}
			if cur != "" {
				served = true
			}
		}
		if headErr != nil {
			if _, err := h.Registry.GetSite(opCtx, slug); err == nil {
				auditDeleteFailure("unpublish")
				writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.delete", headErr)
				wrote = true
				return nil
			}
		}
		for _, mode := range modes {
			if err := h.R2.DeleteAlias(opCtx, h.aliasKey(slug, mode)); err != nil {
				auditDeleteFailure("unpublish")
				writeUpstreamError(w, r, http.StatusBadGateway, "r2_delete_failed", "r2.delete.alias", err)
				wrote = true
				return nil
			}
		}
		until := h.Now().UTC().Add(h.ReservationGrace)
		if _, err := h.Reservations.Reserve(opCtx, slug, dirname, until, LoginFromContext(r.Context()), observed); err != nil {
			if errors.Is(err, registry.ErrNotFound) && (served || headErr != nil) {
				servedOrphan = true
				orphanObserved = served
				probeUnreadable = headErr != nil
				return nil
			}
			auditDeleteFailure("reserve")
			writeRegistryDeleteError(w, r, err)
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
	if servedOrphan {
		detail := map[string]any{"orphan": orphanObserved, "reserved": false}
		body := map[string]any{"slug": string(slug), "status": "unpublished", "reserved": false}
		if probeUnreadable {
			detail["aliasProbe"] = "unreadable"
			body["aliasProbe"] = "unreadable"
		}
		h.logAction(r.Context(), "site.delete", "success",
			slog.Bool("orphan", orphanObserved), slog.Bool("aliasProbeUnreadable", probeUnreadable))
		h.auditFromScope(r.Context(), "site.delete", "success", detail)
		writeJSON(w, http.StatusOK, body)
		return
	}
	h.logAction(r.Context(), "site.delete", "success")
	h.auditFromScope(r.Context(), "site.delete", "success", nil)
	w.WriteHeader(http.StatusNoContent)
}

// SiteUndelete implements POST /api/site/{slug}/undelete — returns a
// reserved name to its owner before the grace period expires. Authz:
// caller in h.RegistryAuthzTeam.
func (h *Handlers) SiteUndelete(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}
	slug := sitekey.Slug(chi.URLParam(r, "slug"))
	if !slugRe.MatchString(string(slug)) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}
	reverser, ok := h.Registry.(ReservationReverser)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "reservation store not configured")
		return
	}
	// The lock is shared with SiteRelease, which trashes the origin bytes
	// before it frees the name. An undelete landing inside that window
	// would return an emptied site to service.
	var res registry.Reservation
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)
	defer cancel()
	var undeleteErr error
	var undeleteStage string
	lockErr := h.withSiteLock(opCtx, h.DeployPrefix.SiteDirname(slug), func(opCtx context.Context) error {
		held, err := reverser.Reservation(opCtx, slug)
		if err != nil {
			undeleteErr, undeleteStage = err, "read"
			return nil
		}
		if stage, err := h.restoreAliasPins(opCtx, slug, held); err != nil {
			undeleteErr, undeleteStage = err, stage
			return nil
		}
		res, err = reverser.Undelete(opCtx, slug)
		if err != nil {
			undeleteErr, undeleteStage = err, "undelete"
		}
		return nil
	})
	if lockErr != nil {
		writeLockError(w, r, lockErr)
		return
	}
	if undeleteErr != nil {
		telemetry.FromContext(r.Context()).SetResource(string(slug), "")
		h.auditFromScope(r.Context(), "site.undelete", "failure", map[string]any{"stage": undeleteStage})
		if undeleteStage == "restore_alias" {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.alias-undelete", undeleteErr)
			return
		}
		writeRegistryDeleteError(w, r, undeleteErr)
		return
	}
	telemetry.FromContext(r.Context()).SetResource(string(slug), "")
	h.logAction(r.Context(), "site.undelete", "success")
	h.auditFromScope(r.Context(), "site.undelete", "success", nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":           string(res.Slug),
		"prevProduction": res.PrevProduction,
		"prevPreview":    res.PrevPreview,
	})
}

func (h *Handlers) restoreAliasPins(ctx context.Context, slug sitekey.Slug, held registry.Reservation) (string, error) {
	pins := []struct {
		mode     string
		deployID string
	}{
		{"production", held.PrevProduction},
		{"preview", held.PrevPreview},
	}
	dirname := h.DeployPrefix.SiteDirname(slug)
	for _, pin := range pins {
		if pin.deployID == "" {
			continue
		}
		if err := h.R2.PutAlias(ctx, h.aliasKey(slug, pin.mode), pin.deployID); err != nil {
			return "restore_alias", fmt.Errorf("undelete restore %s alias %s: %w", pin.mode, slug, err)
		}
		if h.Index == nil {
			continue
		}
		if err := h.Index.AliasAtomic(ctx, dirname, pin.mode, pin.deployID, h.Now().UTC()); err != nil {
			return "restore_index", fmt.Errorf("undelete restore %s alias row %s: %w", pin.mode, slug, err)
		}
	}
	return "", nil
}
