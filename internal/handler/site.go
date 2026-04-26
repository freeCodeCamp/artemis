package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

// SitePromote implements POST /api/site/{site}/promote — copies the
// preview alias content (a deploy id pointer) to the production alias.
// Atomic single-PUT semantics.
func (h *Handlers) SitePromote(w http.ResponseWriter, r *http.Request) {
	site := chi.URLParam(r, "site")
	if err := h.requireSiteAuthz(w, r, site); err != nil {
		return // already wrote response
	}

	previewKey := h.aliasKey(site, "preview")
	deployID, err := h.R2.GetAlias(r.Context(), previewKey)
	if err != nil {
		if r2.IsNotFound(err) {
			writeError(w, http.StatusUnprocessableEntity, "no_preview", "no preview alias to promote")
			return
		}
		writeError(w, http.StatusBadGateway, "r2_get_failed", err.Error())
		return
	}
	deployID = strings.TrimSpace(deployID)
	if deployID == "" {
		writeError(w, http.StatusUnprocessableEntity, "no_preview", "preview alias is empty")
		return
	}

	prodKey := h.aliasKey(site, "production")
	if err := h.R2.PutAlias(r.Context(), prodKey, deployID); err != nil {
		writeError(w, http.StatusBadGateway, "r2_put_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":      h.publicURL(site, "production"),
		"deployId": deployID,
	})
}

// SiteRollbackRequest is the body of /api/site/{site}/rollback.
type SiteRollbackRequest struct {
	To string `json:"to"`
}

// SiteRollback implements POST /api/site/{site}/rollback — rewrites the
// production alias to point at a past deploy id. Refuses if the target
// deploy prefix has no objects (i.e. has been swept by the cleanup cron).
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
	if req.To == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "to is required")
		return
	}

	prefix := h.deployPrefix(site, req.To)
	keys, err := h.R2.ListPrefix(r.Context(), prefix)
	if err != nil {
		writeError(w, http.StatusBadGateway, "r2_list_failed", err.Error())
		return
	}
	if len(keys) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "deploy_missing", "target deploy no longer exists in r2")
		return
	}

	prodKey := h.aliasKey(site, "production")
	if err := h.R2.PutAlias(r.Context(), prodKey, req.To); err != nil {
		writeError(w, http.StatusBadGateway, "r2_put_failed", err.Error())
		return
	}

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
		writeError(w, http.StatusBadGateway, "r2_list_failed", err.Error())
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
