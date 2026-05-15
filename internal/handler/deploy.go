package handler

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/go-chi/chi/v5"
)

// DeployInitRequest is the body of POST /api/deploy/init.
type DeployInitRequest struct {
	Site  string   `json:"site"`
	SHA   string   `json:"sha"`
	Files []string `json:"files,omitempty"` // optional manifest used by /finalize
}

// DeployInitResponse is the success payload of /api/deploy/init.
type DeployInitResponse struct {
	DeployID  string `json:"deployId"`
	JWT       string `json:"jwt"`
	ExpiresAt string `json:"expiresAt"`
}

// DeployInit implements POST /api/deploy/init.
//
// Preconditions: caller must have passed RequireGitHubBearer (login on
// context). The handler additionally enforces that the requested site
// exists in the registry and that the caller's team membership grants
// access to it.
func (h *Handlers) DeployInit(w http.ResponseWriter, r *http.Request) {
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())

	var req DeployInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return
	}
	if req.Site == "" || req.SHA == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "site and sha are required")
		return
	}

	teams := h.Sites.Snapshot().TeamsForSite(req.Site)
	if len(teams) == 0 {
		writeError(w, http.StatusForbidden, "site_unauthorized", "site is not registered or has no authorized teams")
		return
	}

	ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, teams)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized", "user is not on any authorized team for this site")
		return
	}

	deployID := h.NewDeployID(req.SHA)
	tok, exp, err := h.JWT.Sign(login, req.Site, deployID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "jwt_sign_failed", "could not sign deploy-session jwt")
		return
	}

	writeJSON(w, http.StatusOK, DeployInitResponse{
		DeployID:  deployID,
		JWT:       tok,
		ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// DeployUpload implements PUT /api/deploy/{deployId}/upload?path=...
//
// Preconditions: RequireDeployJWT must have populated the JWT claims on
// the context. The URL `deployId` must match the JWT-scoped deployId.
func (h *Handlers) DeployUpload(w http.ResponseWriter, r *http.Request) {
	claims, ok := JWTClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing_jwt", "deploy-session jwt missing")
		return
	}
	deployID := chi.URLParam(r, "deployId")
	if err := claims.RequireScope(claims.Subject, claims.Site, deployID); err != nil {
		writeError(w, http.StatusForbidden, "jwt_wrong_deploy", "deploy-session jwt does not match url deploy id")
		return
	}

	relPath := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing ?path=")
		return
	}
	if !isCleanRelPath(relPath) {
		writeError(w, http.StatusBadRequest, "bad_request", "path must be relative and not traverse")
		return
	}

	prefix := h.deployPrefix(claims.Site, deployID)
	key := prefix + relPath

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(relPath))
	}
	if contentType == "" {
		// B23: explicit fallback so every R2 object stores a
		// Content-Type. Browser default is `application/octet-stream`
		// anyway; setting it explicitly avoids R2's missing-header
		// behavior and makes object metadata complete.
		contentType = "application/octet-stream"
	}

	body := r.Body
	if h.UploadMaxBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, h.UploadMaxBytes)
	}

	// Propagate ContentLength when the client sent one. Avoids chunked
	// transfer-encoding negotiation on small uploads. Zero or negative
	// → unknown; SDK falls back to its default behavior.
	contentLength := r.ContentLength
	if contentLength < 0 {
		contentLength = 0
	}

	if err := h.R2.PutObject(r.Context(), key, body, contentType, contentLength); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large",
				"upload body exceeds configured limit")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.upload", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"received": relPath,
		"key":      key,
	})
}

// DeployFinalizeRequest is the body of /api/deploy/{deployId}/finalize.
type DeployFinalizeRequest struct {
	Mode  string   `json:"mode"`            // "preview" or "production"
	Files []string `json:"files,omitempty"` // expected file list — all must surface under the deploy prefix
}

// DeployFinalize implements POST /api/deploy/{deployId}/finalize.
//
// Atomic alias semantics: the handler first verifies via ListObjectsV2
// that every expected file landed in R2, then performs a single PUT to
// the alias key. The previous deploy keeps serving until the alias PUT
// completes; a partial deploy never becomes live.
func (h *Handlers) DeployFinalize(w http.ResponseWriter, r *http.Request) {
	claims, ok := JWTClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing_jwt", "deploy-session jwt missing")
		return
	}
	deployID := chi.URLParam(r, "deployId")
	if err := claims.RequireScope(claims.Subject, claims.Site, deployID); err != nil {
		writeError(w, http.StatusForbidden, "jwt_wrong_deploy", "deploy-session jwt does not match url deploy id")
		return
	}

	var req DeployFinalizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return
	}
	mode, err := normalizeMode(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "manifest_required",
			"files manifest is required and must list at least one path")
		return
	}

	prefix := h.deployPrefix(claims.Site, deployID)
	if err := h.R2.VerifyDeployComplete(r.Context(), prefix, req.Files); err != nil {
		var verr *r2.VerifyError
		if errors.As(err, &verr) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": map[string]any{
					"code":    "verify_failed",
					"message": "deploy is missing expected files",
					"missing": verr.Missing,
				},
			})
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_list_failed", "r2.list.verify", err)
		return
	}

	aliasKey := h.aliasKey(claims.Site, mode)
	if err := h.R2.PutAlias(r.Context(), aliasKey, deployID); err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.alias.finalize", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"url":      h.publicURL(claims.Site, mode),
		"deployId": deployID,
		"mode":     mode,
	})
}

// deployPrefix returns the R2 key prefix for one deploy, e.g.
// "www/deploys/20260420-141522-abc1234/".
func (h *Handlers) deployPrefix(site, deployID string) string {
	return h.DeployPrefix.DeployPrefix(site, deployID)
}

// aliasKey returns the R2 alias key for `mode` ("preview"/"production").
func (h *Handlers) aliasKey(site, mode string) string {
	switch mode {
	case "production":
		return strings.ReplaceAll(h.AliasProductionFmt, "<site>", site)
	default:
		return strings.ReplaceAll(h.AliasPreviewFmt, "<site>", site)
	}
}

// publicURL returns the user-visible URL for a finalized deploy.
func (h *Handlers) publicURL(site, mode string) string {
	if h.PublicURLForSite != nil {
		return h.PublicURLForSite(site, mode)
	}
	if mode == "production" {
		return "https://" + site + ".freecode.camp"
	}
	return "https://" + site + ".preview.freecode.camp"
}

// normalizeMode validates and normalizes finalize/promote/rollback `mode` arg.
func normalizeMode(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", "preview":
		return "preview", nil
	case "production":
		return "production", nil
	default:
		return "", errors.New(`mode must be "preview" or "production"`)
	}
}

// isCleanRelPath rejects empty / absolute / traversal / current-dir
// paths plus paths containing ASCII control bytes or backslashes.
//
// Reject rationale:
//   - empty / ".": creates a malformed `<deploy-prefix>.` key on R2.
//   - absolute / "..": classic traversal; the user-controlled relPath
//     would otherwise escape the per-deploy prefix.
//   - control bytes (<0x20 or 0x7F): null bytes (\x00) silently truncate
//     in some downstream tooling; newlines / tabs enable log-injection
//     against artemis access logs.
//   - backslash: never legal in an R2 key segment in the deploy schema;
//     accepting it makes Caddy + R2 disagree on the canonical key.
//
// High-bit UTF-8 codepoints (≥0x80) are accepted — artemis serves
// static apps whose filenames may include non-ASCII characters.
func isCleanRelPath(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "/") {
		return false
	}
	for i := 0; i < len(p); i++ {
		b := p[i]
		if b < 0x20 || b == 0x7F || b == '\\' {
			return false
		}
	}
	cleaned := path.Clean(p)
	if cleaned != p {
		return false
	}
	if strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") || cleaned == ".." {
		return false
	}
	return true
}
