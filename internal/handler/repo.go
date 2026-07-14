package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/getsentry/sentry-go"
	"github.com/go-chi/chi/v5"

	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/reporequest"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
)

const maxRepoDescriptionLen = 350

// RepoStore is the repo-request queue contract used by the repo handlers.
type RepoStore interface {
	Create(ctx context.Context, req reporequest.Request) (reporequest.Request, error)
	Get(ctx context.Context, id string) (reporequest.Request, error)
	List(ctx context.Context) ([]reporequest.Request, error)
	Approve(ctx context.Context, id, approver string) (reporequest.Request, error)
	Reject(ctx context.Context, id, approver, reason string) (reporequest.Request, error)
	MarkActive(ctx context.Context, id, url string) (reporequest.Request, error)
	MarkFailed(ctx context.Context, id, errMsg string) (reporequest.Request, error)
	MarkStale(ctx context.Context, id, reason string) (reporequest.Request, error)
	Delete(ctx context.Context, id string) error
}

// RepoCreator is the GitHub App contract used on approve + templates.
type RepoCreator interface {
	CreateRepo(ctx context.Context, spec githubapp.CreateSpec) (githubapp.Created, error)
	ListTemplates(ctx context.Context) ([]string, error)
	RepoExists(ctx context.Context, name string) (bool, string, error)
}

// RepoRow is the camelCase JSON shape for a repo request — stable across
// create / list / get / approve / reject responses (dossier §I/§V6).
type RepoRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Owner        string    `json:"owner"`
	Visibility   string    `json:"visibility"`
	Description  string    `json:"description,omitempty"`
	Template     string    `json:"template,omitempty"`
	Status       string    `json:"status"`
	URL          string    `json:"url,omitempty"`
	Error        string    `json:"error,omitempty"`
	RequestedBy  string    `json:"requestedBy"`
	Approver     string    `json:"approver,omitempty"`
	RejectReason string    `json:"rejectReason,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func toRepoRow(r reporequest.Request) RepoRow {
	return RepoRow{
		ID:           r.ID,
		Name:         r.Name,
		Owner:        r.Owner,
		Visibility:   string(r.Visibility),
		Description:  r.Description,
		Template:     r.Template,
		Status:       string(r.Status),
		URL:          r.URL,
		Error:        r.Error,
		RequestedBy:  r.RequestedBy,
		Approver:     r.Approver,
		RejectReason: r.RejectReason,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// RepoCreateRequest is the body of POST /api/repo.
type RepoCreateRequest struct {
	Name        string `json:"name"`
	Visibility  string `json:"visibility,omitempty"`
	Description string `json:"description,omitempty"`
	Template    string `json:"template,omitempty"`
}

// RepoRejectRequest is the (optional) body of POST /api/repo/{id}/reject.
type RepoRejectRequest struct {
	Reason string `json:"reason,omitempty"`
}

// RepoApproveResponse is the 200 body of POST /api/repo/{id}/approve.
// Outcome is "ok" (repo created, status active) or "approved_failed"
// (approval recorded but GitHub creation failed, status failed).
type RepoApproveResponse struct {
	Outcome string  `json:"outcome"`
	Request RepoRow `json:"request"`
}

// RepoTemplatesResponse is the 200 body of GET /api/repo/templates.
type RepoTemplatesResponse struct {
	Templates []string `json:"templates"`
}

// RepoEnabled reports whether the repo-creation feature has its
// dependencies wired (queue store, GitHub App client, Universe-org
// membership prober). The server mounts /api/repo* only when true, so
// deploy-only deployments without the Apollo-11 key run unaffected.
func (h *Handlers) RepoEnabled() bool {
	return h.Repos != nil && h.GitHubApp != nil && h.RepoGH != nil
}

// RepoCreate implements POST /api/repo — submits a repo-creation request
// to the approval queue. Authz: caller on RepoCreateAuthzTeam.
//
//	201 Created     — body = RepoRow (status pending)
//	400 Bad Request — invalid name / visibility / template / json
//	403 Forbidden   — caller not on the create team
//	409 Conflict    — a request for this name is already pending/active
//	502 Bad Gateway — queue write failed
func (h *Handlers) RepoCreate(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRepoTeam(w, r, h.RepoCreateAuthzTeam); err != nil {
		return
	}
	var req RepoCreateRequest
	if !decodeJSON(w, r, &req, maxJSONBodyBytes) {
		return
	}
	if !reporequest.ValidName(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name",
			"repo name must start with a letter or digit, then letters, digits, '.', '_' or '-' (max 100 chars)")
		return
	}
	vis := reporequest.Visibility(req.Visibility)
	if req.Visibility == "" {
		vis = reporequest.VisibilityPrivate
	}
	if !vis.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_visibility", "visibility must be public or private")
		return
	}
	if req.Template != "" && !reporequest.ValidName(req.Template) {
		writeError(w, http.StatusBadRequest, "invalid_template", "template must be an existing org repo name")
		return
	}
	if utf8.RuneCountInString(req.Description) > maxRepoDescriptionLen {
		writeError(w, http.StatusBadRequest, "invalid_description",
			"description must be 350 characters or fewer")
		return
	}

	login := LoginFromContext(r.Context())
	newReq := reporequest.Request{
		Name:        req.Name,
		Owner:       h.RepoOrg,
		Visibility:  vis,
		Description: req.Description,
		Template:    req.Template,
		RequestedBy: login,
	}
	created, err := h.Repos.Create(r.Context(), newReq)
	if errors.Is(err, reporequest.ErrAlreadyExists) && h.reconcileStaleClaim(r, req.Name) {
		created, err = h.Repos.Create(r.Context(), newReq)
	}
	if err != nil {
		if errors.Is(err, reporequest.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "already_exists",
				"a request for this repo name is already pending or active")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.create", err)
		return
	}
	slog.InfoContext(r.Context(), "repo.create.queued", "id", created.ID, "name", req.Name, "owner", h.RepoOrg, "visibility", string(vis))
	h.auditFromScope(r.Context(), "repo.create", "success", map[string]any{"id": created.ID, "name": req.Name})
	writeJSON(w, http.StatusCreated, toRepoRow(created))
}

func (h *Handlers) reconcileStaleClaim(r *http.Request, name string) bool {
	all, err := h.Repos.List(r.Context())
	if err != nil {
		return false
	}
	for _, req := range all {
		if !strings.EqualFold(req.Name, name) {
			continue
		}
		switch req.Status {
		case reporequest.StatusActive:
			exists, _, gErr := h.GitHubApp.RepoExists(r.Context(), req.Name)
			if gErr != nil {
				slog.WarnContext(r.Context(), "repo.create.reconcile_probe_failed", "id", req.ID, "name", req.Name, "err", gErr)
				return false
			}
			if exists {
				return false
			}
			if _, mErr := h.Repos.MarkStale(r.Context(), req.ID, "repository no longer exists on GitHub; name claim reconciled"); mErr != nil {
				return false
			}
			slog.WarnContext(r.Context(), "repo.create.reconciled_stale_claim", "id", req.ID, "name", req.Name)
			return true
		case reporequest.StatusPending, reporequest.StatusApproved:
			return false
		}
	}
	return false
}

// ReposList implements GET /api/repos — lists requests. Query params:
// `status` (default "pending"; "all" for no filter) and `mine` (1/true
// → only the caller's requests). Open to any GitHub bearer.
//
//	200 OK           — body = []RepoRow
//	400 Bad Request  — invalid status filter
//	502 Bad Gateway  — queue read failed
func (h *Handlers) ReposList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = string(reporequest.StatusPending)
	}
	if status != "all" && !reporequest.Status(status).Valid() {
		writeError(w, http.StatusBadRequest, "invalid_status",
			"status must be one of pending, approved, active, rejected, failed, all")
		return
	}
	mineParam := r.URL.Query().Get("mine")
	mine := mineParam == "1" || mineParam == "true"
	login := LoginFromContext(r.Context())

	all, err := h.Repos.List(r.Context())
	if err != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.list", err)
		return
	}
	rows := make([]RepoRow, 0, len(all))
	for _, req := range all {
		if status != "all" && string(req.Status) != status {
			continue
		}
		if mine && req.RequestedBy != login {
			continue
		}
		rows = append(rows, toRepoRow(req))
	}
	writeJSON(w, http.StatusOK, rows)
}

// RepoGet implements GET /api/repo/{id}. Open to any GitHub bearer.
//
//	200 OK          — body = RepoRow
//	404 Not Found   — no request with that id
//	502 Bad Gateway — queue read failed
func (h *Handlers) RepoGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	req, err := h.Repos.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, reporequest.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "repo request not found")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.get", err)
		return
	}
	writeJSON(w, http.StatusOK, toRepoRow(req))
}

// RepoApprove implements POST /api/repo/{id}/approve. Authz: caller on
// RepoApproveAuthzTeam. The approval is a CAS pending→approved (only one
// of several racing admins wins); on success the handler synchronously
// mints the Apollo-11 token and creates the repo, then resolves the row
// to active or failed.
//
//	200 OK            — body = RepoApproveResponse (ok | approved_failed)
//	403 Forbidden     — caller not on the approve team
//	404 Not Found     — no request with that id
//	409 Conflict      — already resolved by another admin
//	502 Bad Gateway   — queue write failed
func (h *Handlers) RepoApprove(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRepoTeam(w, r, h.RepoApproveAuthzTeam); err != nil {
		return
	}
	id := chi.URLParam(r, "id")
	login := LoginFromContext(r.Context())
	slog.InfoContext(r.Context(), "repo.approve.start", "id", id)

	approved, err := h.Repos.Approve(r.Context(), id, login)
	resume := false
	switch {
	case err == nil:
		// fresh pending → approved transition
	case errors.Is(err, reporequest.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "repo request not found")
		return
	case errors.Is(err, reporequest.ErrNotPending):
		// The CAS rejected a non-pending row. If it is stranded in
		// `approved` (a prior attempt created the repo but never recorded
		// the outcome — e.g. a canceled durable write), resume and
		// reconcile; otherwise it is genuinely resolved (PR freeCodeCamp/artemis#3, #7).
		cur, gErr := h.Repos.Get(r.Context(), id)
		if gErr != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.get", gErr)
			return
		}
		if cur.Status != reporequest.StatusApproved {
			writeError(w, http.StatusConflict, "already_resolved",
				"request was already resolved by another admin")
			return
		}
		approved, resume = cur, true
		slog.WarnContext(r.Context(), "repo.approve.resume_stranded_approved", "id", id)
	default:
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.approve", err)
		return
	}

	// Once GitHub has the side effect, the durable status write must
	// complete even if the client disconnects, else the repo exists while
	// the row stays `approved` with no resolution (PR freeCodeCamp/artemis#3, #2).
	durCtx := context.WithoutCancel(r.Context())

	telemetry.Breadcrumb(r.Context(), "repo", "github repo create")
	var created githubapp.Created
	ghErr := telemetry.WithSpan(durCtx, "githubapp.createrepo", func(ctx context.Context) error {
		var e error
		created, e = h.GitHubApp.CreateRepo(ctx, githubapp.CreateSpec{
			Name:        approved.Name,
			Private:     approved.Visibility == reporequest.VisibilityPrivate,
			Description: approved.Description,
			Template:    approved.Template,
		})
		return e
	})
	var existsErr *githubapp.RepoExistsError
	if errors.As(ghErr, &existsErr) && resume {
		// Our prior attempt already created the repo; finish the reconcile.
		created, ghErr = githubapp.Created{URL: existsErr.URL}, nil
	}
	if ghErr != nil {
		if resume && githubapp.IsTransient(ghErr) {
			writeUpstreamError(w, r, http.StatusServiceUnavailable, "repo_create_retryable", "githubapp.createrepo.transient", ghErr)
			return
		}
		msg := "repository creation failed"
		var uf *githubapp.UserFacingError
		switch {
		case errors.As(ghErr, &existsErr):
			msg = existsErr.Error()
			slog.WarnContext(r.Context(), "repo.approve.github_rejected", "outcome", "approved_failed", "id", id, "name", approved.Name, "reason", "repo_exists")
		case errors.As(ghErr, &uf):
			msg = uf.Error()
			slog.WarnContext(r.Context(), "repo.approve.github_rejected", "outcome", "approved_failed", "id", id, "name", approved.Name, "reason", uf.Error())
		default:
			slog.ErrorContext(r.Context(), "repo.create.upstream_error",
				"op", "githubapp.createrepo",
				"err", ghErr,
				"id", id,
			)
			if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
				hub.WithScope(func(scope *sentry.Scope) {
					scope.SetTag("op", "githubapp.createrepo")
					scope.SetFingerprint([]string{"upstream", "githubapp.createrepo"})
					hub.CaptureException(ghErr)
				})
			}
		}
		failed, mErr := h.Repos.MarkFailed(durCtx, id, msg)
		if mErr != nil {
			writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.markfailed", mErr)
			return
		}
		h.auditFromScope(durCtx, "repo.approve", "approved_failed", map[string]any{"id": id, "name": approved.Name})
		writeJSON(w, http.StatusOK, RepoApproveResponse{Outcome: "approved_failed", Request: toRepoRow(failed)})
		return
	}

	active, mErr := h.Repos.MarkActive(durCtx, id, created.URL)
	if mErr != nil {
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.markactive", mErr)
		return
	}
	slog.InfoContext(r.Context(), "repo.approve.created", "id", id, "name", active.Name, "url", created.URL)
	h.auditFromScope(durCtx, "repo.approve", "success", map[string]any{"id": id, "name": active.Name, "url": created.URL})
	writeJSON(w, http.StatusOK, RepoApproveResponse{Outcome: "ok", Request: toRepoRow(active)})
}

// RepoReject implements POST /api/repo/{id}/reject. Authz: caller on
// RepoApproveAuthzTeam. Optional body {reason}. CAS-guarded like approve.
//
//	200 OK          — body = RepoRow (status rejected)
//	400 Bad Request — non-empty body that is not valid json
//	403 Forbidden   — caller not on the approve team
//	404 Not Found   — no request with that id
//	409 Conflict    — already resolved by another admin
//	502 Bad Gateway — queue write failed
func (h *Handlers) RepoReject(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRepoTeam(w, r, h.RepoApproveAuthzTeam); err != nil {
		return
	}
	id := chi.URLParam(r, "id")

	var body RepoRejectRequest
	// Body is optional (empty → zero reason), but a malformed non-empty
	// body must not be silently discarded into a reject with no reason.
	if !decodeJSONOptional(w, r, &body, maxJSONBodyBytes) {
		return
	}

	login := LoginFromContext(r.Context())
	rejected, err := h.Repos.Reject(r.Context(), id, login, body.Reason)
	if err != nil {
		switch {
		case errors.Is(err, reporequest.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "repo request not found")
		case errors.Is(err, reporequest.ErrNotPending):
			writeError(w, http.StatusConflict, "already_resolved",
				"request was already resolved by another admin")
		default:
			writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.reject", err)
		}
		return
	}
	slog.InfoContext(r.Context(), "repo.reject.recorded", "id", id, "name", rejected.Name, "reason", body.Reason)
	h.auditFromScope(r.Context(), "repo.reject", "success", map[string]any{"id": id, "name": rejected.Name, "reason": body.Reason})
	writeJSON(w, http.StatusOK, toRepoRow(rejected))
}

func (h *Handlers) RepoDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRepoTeam(w, r, h.RepoApproveAuthzTeam); err != nil {
		return
	}
	id := chi.URLParam(r, "id")
	row, err := h.Repos.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, reporequest.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "repo request not found")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.get", err)
		return
	}
	if err := h.Repos.Delete(r.Context(), id); err != nil {
		if errors.Is(err, reporequest.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "repo request not found")
			return
		}
		writeUpstreamError(w, r, http.StatusBadGateway, "repo_store_failed", "valkey.repo.delete", err)
		return
	}
	slog.InfoContext(r.Context(), "repo.delete.removed", "id", id, "name", row.Name)
	h.auditFromScope(r.Context(), "repo.delete", "success", map[string]any{"id": id, "name": row.Name})
	w.WriteHeader(http.StatusNoContent)
}

// RepoTemplates implements GET /api/repo/templates — the allowlisted
// org templates the App can clone. Open to any GitHub bearer. Fail-soft:
// any upstream error yields an empty list (200) so the CLI degrades to
// free-text template entry rather than blocking the create flow.
func (h *Handlers) RepoTemplates(w http.ResponseWriter, r *http.Request) {
	names, err := h.GitHubApp.ListTemplates(r.Context())
	if err != nil {
		slog.WarnContext(r.Context(), "repo.templates.failed",
			"err", err)
		writeJSON(w, http.StatusOK, RepoTemplatesResponse{Templates: []string{}})
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, RepoTemplatesResponse{Templates: names})
}

// requireRepoTeam enforces that the authenticated caller is on `team`
// IN THE UNIVERSE ORG via h.RepoGH (NOT h.GH, which is scoped to the
// site-registry org — dossier §V4). Writes the response on failure and
// returns a non-nil error so the caller early-returns.
func (h *Handlers) requireRepoTeam(w http.ResponseWriter, r *http.Request, team string) error {
	if team == "" {
		writeError(w, http.StatusInternalServerError, "misconfigured", "repo authz team not set")
		return errBadRequest
	}
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())
	ok, err := h.RepoGH.AuthorizeForSite(r.Context(), token, login, []string{team})
	if err != nil {
		writeGitHubProbeError(w, err)
		return err
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized", "caller is not on the required team")
		return errBadRequest
	}
	return nil
}
