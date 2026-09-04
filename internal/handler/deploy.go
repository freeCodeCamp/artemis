package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/go-chi/chi/v5"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

// DeployInitRequest is the body of POST /api/deploy/init.
type DeployInitRequest struct {
	Site  sitekey.Slug `json:"site"`
	SHA   string       `json:"sha"`
	Files []string     `json:"files,omitempty"` // optional manifest used by /finalize
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
	if !decodeJSON(w, r, &req, maxManifestBodyBytes) {
		return
	}
	if req.Site == "" || req.SHA == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "site and sha are required")
		return
	}
	if !shaPattern.MatchString(req.SHA) {
		writeError(w, http.StatusBadRequest, "bad_request", "sha must match [A-Za-z0-9-]{1,64}")
		return
	}

	telemetry.FromContext(r.Context()).SetResource(string(req.Site), "")
	h.logAction(r.Context(), "deploy.init", "start", slog.String("sha", req.SHA))

	teams := h.Sites.Snapshot().TeamsForSite(req.Site)
	if len(teams) == 0 {
		reason := h.denyUnregisteredSite(w, r, req.Site)
		h.logAction(r.Context(), "deploy.init", "denied", slog.String("reason", reason))
		return
	}

	ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, teams)
	if err != nil {
		h.logAction(r.Context(), "deploy.init", "error", slog.String("reason", "authz_probe_failed"))
		writeGitHubProbeError(w, err)
		return
	}
	if !ok {
		h.logAction(r.Context(), "deploy.init", "denied", slog.String("reason", "user_unauthorized"))
		writeError(w, http.StatusForbidden, "user_unauthorized", "user is not on any authorized team for this site")
		return
	}

	deployID := h.NewDeployID(req.SHA)
	tok, exp, err := h.JWT.Sign(login, req.Site, deployID)
	if err != nil {
		h.logAction(r.Context(), "deploy.init", "error", slog.String("reason", "jwt_sign_failed"))
		writeError(w, http.StatusInternalServerError, "jwt_sign_failed", "could not sign deploy-session jwt")
		return
	}

	telemetry.FromContext(r.Context()).SetResource(string(req.Site), deployID)
	h.beginPendingDeploy(r.Context(), h.DeployPrefix.SiteDirname(req.Site), deployID)
	h.logAction(r.Context(), "deploy.init", "success")
	h.auditFromScope(r.Context(), "deploy.init", "success", map[string]any{"sha": req.SHA})

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
	if err := claims.RequireDeployID(deployID); err != nil {
		writeError(w, http.StatusForbidden, "jwt_wrong_deploy", "deploy-session jwt does not match url deploy id")
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing ?path=")
		return
	}
	if !isCleanRelPath(relPath) {
		writeError(w, http.StatusBadRequest, "bad_request", "path must be relative and not traverse")
		return
	}

	if h.DeployFence != nil {
		finalized, err := h.DeployFence.IsDeployFinalized(r.Context(), claims.Site, deployID)
		if err != nil {
			writeUpstreamError(w, r, http.StatusServiceUnavailable, "fence_unavailable",
				"valkey.get.deploy_fence", err)
			return
		}
		if finalized {
			writeError(w, http.StatusConflict, "deploy_finalized",
				"deploy is finalized and immutable; start a new deploy")
			return
		}
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

	telemetry.FromContext(r.Context()).SetResource(string(claims.Site), deployID)
	h.logAction(r.Context(), "deploy.upload", "success",
		slog.String("path", relPath), slog.Int64("bytes", contentLength))
	h.auditFromScope(r.Context(), "deploy.upload", "success", map[string]any{"path": relPath, "bytes": contentLength})
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
	if err := claims.RequireDeployID(deployID); err != nil {
		writeError(w, http.StatusForbidden, "jwt_wrong_deploy", "deploy-session jwt does not match url deploy id")
		return
	}

	var req DeployFinalizeRequest
	if !decodeJSON(w, r, &req, maxManifestBodyBytes) {
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
	if !hasIndexHTML(req.Files) {
		var extra map[string]any
		if hint := frameworkBuildHint(req.Files); hint != "" {
			extra = map[string]any{"hint": hint}
		}
		writeErrorDetail(w, http.StatusUnprocessableEntity, "missing_index",
			"deploy has no root index.html; the site cannot be served at /", extra)
		return
	}

	prefix := h.deployPrefix(claims.Site, deployID)
	if err := telemetry.WithSpan(r.Context(), "r2.list.verify", func(ctx context.Context) error {
		return h.R2.VerifyDeployComplete(ctx, prefix, req.Files)
	}); err != nil {
		var verr *r2.VerifyError
		if errors.As(err, &verr) {
			writeErrorDetail(w, http.StatusUnprocessableEntity, "verify_failed",
				"deploy is missing expected files", map[string]any{"missing": verr.Missing})
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "r2_list_failed", "r2.list.verify", err)
		return
	}
	telemetry.Breadcrumb(r.Context(), "deploy", "deploy manifest verified")

	markerKey := prefix + gc.MarkerObjectName
	meta := fmt.Sprintf(`{"site":%q,"deployId":%q,"mode":%q,"finalizedAt":%q}`,
		string(claims.Site), deployID, mode, time.Now().UTC().Format(time.RFC3339))

	var deployBytes int64
	var touched aliasTouch
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(r.Context()), aliasCommitTimeout)
	defer cancelCommit()
	auditFinalizeFailure := func(stage string) {
		telemetry.FromContext(r.Context()).SetResource(string(claims.Site), deployID)
		h.auditFromScope(r.Context(), "deploy.finalize", "failure",
			map[string]any{"stage": stage, "mode": mode})
	}
	lockErr := h.withSiteLock(commitCtx, h.DeployPrefix.SiteDirname(claims.Site), func(commitCtx context.Context) error {
		telemetry.Breadcrumb(commitCtx, "lock", "site lock acquired")
		site, err := h.requireWritableSite(commitCtx, claims.Site)
		if err != nil {
			if !errors.Is(err, registry.ErrNotFound) && !errors.Is(err, registry.ErrReserved) {
				auditFinalizeFailure("registry")
			}
			h.writeFenceError(w, r, "registry.get.finalize",
				"site was deleted; deploy cannot be finalized", site, err)
			return errAliasWriteHandled
		}
		if err := telemetry.WithSpan(commitCtx, "r2.put.marker.finalize", func(ctx context.Context) error {
			return h.R2.PutObject(ctx, markerKey, strings.NewReader(meta), "application/json", int64(len(meta)))
		}); err != nil {
			auditFinalizeFailure("marker")
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.marker.finalize", err)
			return errAliasWriteHandled
		}
		if bytesErr := telemetry.WithSpan(commitCtx, "r2.list.bytes.finalize", func(ctx context.Context) error {
			var e error
			deployBytes, e = h.R2.PrefixBytes(ctx, prefix)
			return e
		}); bytesErr != nil {
			slog.WarnContext(r.Context(), "deploy.finalize.bytes_unavailable", "err", bytesErr)
			reportUpstream(r, "bytes_unavailable", "r2.list.bytes.finalize", bytesErr)
			deployBytes = 0
		}
		if err := telemetry.WithSpan(commitCtx, "r2.put.alias.finalize", func(ctx context.Context) error {
			return h.putAliasTouched(ctx, &touched, claims.Site, mode, deployID)
		}); err != nil {
			auditFinalizeFailure("alias")
			writeUpstreamError(w, r, http.StatusBadGateway, "r2_put_failed", "r2.put.alias.finalize", err)
			return errAliasWriteHandled
		}
		h.fenceFinalizedDeploy(commitCtx, claims.Site, deployID)
		if h.Index != nil {
			if err := telemetry.WithSpan(commitCtx, "pg.finalize.index", func(ctx context.Context) error {
				return retryIdempotentCommit(ctx, func(ctx context.Context) error {
					return h.Index.FinalizeAtomic(ctx, h.DeployPrefix.SiteDirname(claims.Site), deployID, mode, time.Now().UTC(), deployBytes)
				})
			}); err != nil {
				auditFinalizeFailure("index")
				writeUpstreamError(w, r, http.StatusBadGateway, "pg_write_failed", "pg.finalize.index", err)
				return errAliasWriteHandled
			}
		}
		return nil
	})
	if lockErr != nil {
		if !errors.Is(lockErr, errAliasWriteHandled) {
			writeLockError(w, r, lockErr)
		}
		h.purgeTouched(commitCtx, claims.Site, &touched)
		return
	}
	telemetry.FromContext(r.Context()).SetResource(string(claims.Site), deployID)
	h.logAction(r.Context(), "deploy.finalize", "success",
		slog.String("mode", mode), slog.Int64("bytes", deployBytes))
	h.auditFromScope(r.Context(), "deploy.finalize", "success", map[string]any{"mode": mode, "bytes": deployBytes})
	writeJSON(w, http.StatusOK, map[string]any{
		"url":      h.publicURL(claims.Site, mode),
		"deployId": deployID,
		"mode":     mode,
	})
	h.purgeTouched(commitCtx, claims.Site, &touched)
}

const indexCommitAttempts = 3

const indexCommitBackoff = 150 * time.Millisecond

func retryIdempotentCommit(ctx context.Context, commit func(context.Context) error) error {
	backoff := indexCommitBackoff
	var err error
	for attempt := 1; ; attempt++ {
		err = commit(ctx)
		if err == nil || attempt >= indexCommitAttempts {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		backoff *= 2
	}
}

const rootIndexKey = "index.html"

const staticExportHint = "This looks like a framework build directory, not a static export. " +
	"Configure a static export (e.g. Next.js output: 'export', Nuxt nuxi generate, SvelteKit adapter-static) " +
	"and point platform.yaml build.output at the export directory (e.g. out/, dist/, build/)."

func hasIndexHTML(files []string) bool {
	for _, f := range files {
		if f == rootIndexKey {
			return true
		}
	}
	return false
}

func looksLikeFrameworkBuild(files []string) bool {
	var hasBuildID, hasBuildManifest bool
	for _, f := range files {
		p := strings.ReplaceAll(f, `\`, "/")
		switch p {
		case "BUILD_ID":
			hasBuildID = true
		case "build-manifest.json":
			hasBuildManifest = true
		case "nitro.json":
			return true
		}
		if strings.HasPrefix(p, "_app/immutable/") ||
			strings.HasPrefix(p, ".next/") ||
			strings.HasPrefix(p, ".nuxt/") ||
			strings.HasPrefix(p, ".svelte-kit/") ||
			strings.HasPrefix(p, ".output/") {
			return true
		}
	}
	return hasBuildID && hasBuildManifest
}

func frameworkBuildHint(files []string) string {
	if looksLikeFrameworkBuild(files) {
		return staticExportHint
	}
	return ""
}

// deployPrefix returns the R2 key prefix for one deploy, e.g.
// "www/deploys/20260420-141522-abc1234/".
func (h *Handlers) deployPrefix(site sitekey.Slug, deployID string) string {
	return h.DeployPrefix.DeployPrefix(site, deployID)
}

// aliasKey returns the R2 alias key for `mode` ("preview"/"production").
func (h *Handlers) aliasKey(site sitekey.Slug, mode string) string {
	switch mode {
	case "production":
		return strings.ReplaceAll(h.AliasProductionFmt, "<site>", string(site))
	default:
		return strings.ReplaceAll(h.AliasPreviewFmt, "<site>", string(site))
	}
}

// publicURL returns the user-visible URL for a finalized deploy.
func (h *Handlers) publicURL(site sitekey.Slug, mode string) string {
	if mode == "production" {
		return strings.ReplaceAll(h.PublicProductionURLFmt, "<site>", string(site))
	}
	return strings.ReplaceAll(h.PublicPreviewURLFmt, "<site>", string(site))
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
