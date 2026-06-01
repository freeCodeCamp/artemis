package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

// deployIDPattern matches the artemis deploy id shape
// `YYYYMMDD-HHMMSS-<sha>`. The `<sha>` segment is constrained to
// `[A-Za-z0-9-]{1,64}` — wide enough to accept:
//
//   - git short-sha (7 hex digits) emitted by NewDeployID
//   - `nogit-<base36>` markers from non-git contexts
//   - bespoke test-only prefixes (e.g. `rA86019`) used by the
//     integration suite
//
// and tight enough to reject path separators, dots, control bytes,
// Unicode, and absurd lengths. This matters because the deployId
// flows from the URL / body into R2 key construction in promote /
// rollback paths.
var deployIDPattern = regexp.MustCompile(`^\d{8}-\d{6}-[A-Za-z0-9-]{1,64}$`)

// SitePromoteRequest is the optional body for POST /api/site/{site}/promote.
// Both fields are additive — an empty body keeps the legacy bare-promote
// semantics (read preview alias, copy to production).
type SitePromoteRequest struct {
	DeployID        string `json:"deployId,omitempty"`
	ExpectedCurrent string `json:"expectedCurrent,omitempty"`
}

// SitePromote implements POST /api/site/{site}/promote.
//
// Body schema:
//
//   - empty body — legacy behavior: read preview alias, write that
//     deploy id to the production alias.
//   - {"deployId": "<id>"} — direct-write path: skip the preview read,
//     write the supplied id to production. Eliminates the bare-promote
//     read-then-write race for callers that know which id they want.
//   - {"expectedCurrent": "<id>"} — CAS guard: read current production
//     alias, refuse with 409 alias_drift if it does not match. Combines
//     with deployId so callers can read → diff → swap atomically
//     against the last-observed prod pointer.
//
// Authz: unchanged — staff-team gate enforced by requireSiteAuthz.
func (h *Handlers) SitePromote(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return // already wrote response
	}

	var req SitePromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return
	}
	req.DeployID = strings.TrimSpace(req.DeployID)
	req.ExpectedCurrent = strings.TrimSpace(req.ExpectedCurrent)
	if req.DeployID != "" && !deployIDPattern.MatchString(req.DeployID) {
		writeError(w, http.StatusBadRequest, "bad_request", "deployId is not a valid artemis deploy id")
		return
	}

	// Telemetry-only — empty body keeps legacy semantics for one
	// release per RELEASING.md; the warn surfaces remaining callers
	// before the next sprint flips this branch to 400 Bad Request.
	if req.DeployID == "" && req.ExpectedCurrent == "" {
		slog.Warn("promote.legacy_bare",
			"site", site,
			"remote", r.RemoteAddr,
			"reqID", RequestIDFromContext(r.Context()))
		if h.Metrics != nil {
			h.Metrics.PromoteLegacyBare.Inc()
		}
	}

	prodKey := h.aliasKey(site, "production")

	// CAS guard: read current production alias and bail on mismatch.
	// Treat missing-alias as the empty string so callers can use CAS
	// to assert "no prod yet" by passing ExpectedCurrent="".
	if req.ExpectedCurrent != "" {
		current, err := h.R2.GetAlias(r.Context(), prodKey)
		if err != nil && !r2.IsNotFound(err) {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.promote.cas", err)
			return
		}
		current = strings.TrimSpace(current)
		if current != req.ExpectedCurrent {
			if h.Metrics != nil {
				h.Metrics.AliasDrift.Inc()
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{
					"code":    "alias_drift",
					"message": "production alias has moved since expectedCurrent was read",
				},
				"site":    site,
				"current": current,
			})
			return
		}
	}

	// Resolve the deploy id to write. Explicit param wins; otherwise
	// fall back to the preview alias (legacy path).
	deployID := req.DeployID
	if deployID == "" {
		previewKey := h.aliasKey(site, "preview")
		v, err := h.R2.GetAlias(r.Context(), previewKey)
		if err != nil {
			if r2.IsNotFound(err) {
				writeError(w, http.StatusUnprocessableEntity, "no_preview", "no preview alias to promote")
				return
			}
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.preview", err)
			return
		}
		deployID = strings.TrimSpace(v)
		if deployID == "" {
			writeError(w, http.StatusUnprocessableEntity, "no_preview", "preview alias is empty")
			return
		}
	}

	if err := h.R2.PutAlias(r.Context(), prodKey, deployID); err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.alias.promote", err)
		return
	}

	slog.Info("site.promote", "site", site, "deployId", deployID, "reqID", RequestIDFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"url":      h.publicURL(site, "production"),
		"deployId": deployID,
	})
}

// SiteRollbackRequest is the body of /api/site/{site}/rollback.
//
//   - To is the target deploy id (required).
//   - ExpectedCurrent, when non-empty, gates the rollback on the
//     current production alias matching this value. On mismatch the
//     handler returns 409 alias_drift and refuses to mutate the
//     alias. Mirrors the SitePromoteRequest CAS contract (#28).
type SiteRollbackRequest struct {
	To              string `json:"to"`
	ExpectedCurrent string `json:"expectedCurrent,omitempty"`
}

// SiteRollback implements POST /api/site/{site}/rollback — rewrites the
// production alias to point at a past deploy id. Refuses if the target
// deploy prefix has no objects (i.e. has been swept by the cleanup
// cron) or if ExpectedCurrent is set and disagrees with the current
// production alias body.
func (h *Handlers) SiteRollback(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return
	}

	var req SiteRollbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return
	}
	req.To = strings.TrimSpace(req.To)
	req.ExpectedCurrent = strings.TrimSpace(req.ExpectedCurrent)
	if req.To == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "to is required")
		return
	}
	if !deployIDPattern.MatchString(req.To) {
		writeError(w, http.StatusBadRequest, "bad_request", "to is not a valid artemis deploy id")
		return
	}

	prefix := h.deployPrefix(site, req.To)
	exists, err := h.R2.HasPrefix(r.Context(), prefix)
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_list_failed", "r2.has.prefix.rollback", err)
		return
	}
	if !exists {
		writeError(w, http.StatusUnprocessableEntity, "deploy_missing", "target deploy no longer exists in r2")
		return
	}

	prodKey := h.aliasKey(site, "production")

	// CAS guard: read current prod alias and bail on mismatch. Missing
	// alias normalises to empty-string — symmetric with SitePromote so
	// callers can use a single response shape across both verbs.
	if req.ExpectedCurrent != "" {
		current, err := h.R2.GetAlias(r.Context(), prodKey)
		if err != nil && !r2.IsNotFound(err) {
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_get_failed", "r2.get.alias.rollback.cas", err)
			return
		}
		current = strings.TrimSpace(current)
		if current != req.ExpectedCurrent {
			if h.Metrics != nil {
				h.Metrics.AliasDrift.Inc()
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": map[string]string{
					"code":    "alias_drift",
					"message": "production alias has moved since expectedCurrent was read",
				},
				"site":    site,
				"current": current,
			})
			return
		}
	}

	if err := h.R2.PutAlias(r.Context(), prodKey, req.To); err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.alias.rollback", err)
		return
	}

	slog.Info("site.rollback", "site", site, "to", req.To, "reqID", RequestIDFromContext(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"url":      h.publicURL(site, "production"),
		"deployId": req.To,
	})
}

// SiteDeploys implements GET /api/site/{site}/deploys — lists past
// deploys under <site>/deploys/. Each deploy is identified by the prefix
// segment "<ts>-<sha>".
func (h *Handlers) SiteDeploys(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return
	}

	deploysPrefix := h.DeployPrefix.SitePrefix(site)
	keys, err := h.R2.ListPrefix(r.Context(), deploysPrefix)
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_list_failed", "r2.list.deploys", err)
		return
	}

	// Group by deploy id (first path segment after the prefix).
	seen := map[string]struct{}{}
	deploys := []map[string]any{}
	for _, k := range keys {
		rest := strings.TrimPrefix(k, deploysPrefix)
		segs := strings.SplitN(rest, "/", 2)
		if len(segs) == 0 || segs[0] == "" {
			continue
		}
		id := segs[0]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deploys = append(deploys, map[string]any{"deployId": id})
	}
	writeJSON(w, http.StatusOK, deploys)
}

// requireSiteAuthz enforces that (a) the site is registered and (b) the
// authenticated GitHub user is on at least one of the site's authorized
// teams. Writes the response on failure and returns a non-nil error so
// the caller can early-return without further work.
func (h *Handlers) requireSiteAuthz(w http.ResponseWriter, r *http.Request, site string) error {
	teams := h.Sites.Snapshot().TeamsForSite(site)
	if len(teams) == 0 {
		writeError(w, http.StatusForbidden, "site_unauthorized", "site is not registered or has no authorized teams")
		return errBadRequest
	}
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())
	ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, teams)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
		return err
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized", "user is not on any authorized team for this site")
		return errBadRequest
	}
	return nil
}
