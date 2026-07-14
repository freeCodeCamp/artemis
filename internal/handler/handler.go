// Package handler implements the artemis HTTP API.
//
// Handlers are wired into a chi router by package server. They depend on
// small interfaces (GitHubAuthenticator, DeployJWTSigner, SitesProvider,
// R2Store) so that tests can substitute fakes without booting GitHub or R2.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
)

// GitHubAuthenticator is the subset of *auth.GitHubClient used by the
// handler layer.
type GitHubAuthenticator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
	AuthorizeForSite(ctx context.Context, token, login string, teams []string) (bool, error)
	// UserTeams returns the slugs of every team in the configured org
	// that `token` is a member of. One paginated call replaces N×M
	// per-site IsTeamMember probes in WhoAmI.
	UserTeams(ctx context.Context, token string) ([]string, error)
}

// DeployJWTSigner is the subset of *auth.DeploySessionSigner used by the
// handler layer.
type DeployJWTSigner interface {
	Sign(login, site, deployID string) (string, time.Time, error)
	Verify(token string) (auth.DeploySessionClaims, error)
}

// SitesProvider is the read-side registry contract used by handlers.
// It is an alias of registry.Reader; the indirection lets handler
// tests substitute fakes without importing the registry package
// transitively for the Snapshot type.
type SitesProvider = registry.Reader

// RegistryWriter is the state-mutating registry contract used by
// the /api/site/register and PATCH/DELETE endpoints. Aliasing
// registry.Writer keeps handler tests independent of the concrete
// Valkey backend.
type RegistryWriter = registry.Writer

// R2Store is the subset of *r2.Client used here.
type R2Store interface {
	PutObject(ctx context.Context, key string, body io.Reader, contentType string, contentLength int64) error
	PutAlias(ctx context.Context, aliasKey, deployID string) error
	GetAlias(ctx context.Context, aliasKey string) (string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	HasPrefix(ctx context.Context, prefix string) (bool, error)
	VerifyDeployComplete(ctx context.Context, prefix string, expected []string) error
	MovePrefix(ctx context.Context, src, dst string) (int, error)
	PrefixBytes(ctx context.Context, prefix string) (int64, error)
}

type TombstoneStore interface {
	RecordTombstone(ctx context.Context, site, id string, bytes int64) error
}

type AuditStore interface {
	RecordAudit(ctx context.Context, e pg.AuditEvent) error
}

type TrashStore interface {
	TombstonesForSite(ctx context.Context, site string) ([]gc.Tombstone, error)
	RestoreDeploy(ctx context.Context, site, id string, mtime time.Time, bytes int64) error
}

type SiteChangeEmitter interface {
	EnqueueSiteChanged(ctx context.Context, site string) error
}

type DeployIndexWriter interface {
	FinalizeAtomic(ctx context.Context, site, deployID, mode string, mtime time.Time, bytes int64) error
	AliasAtomic(ctx context.Context, site, name, deployID string, at time.Time) error
}

type SiteLocker interface {
	WithSiteLock(ctx context.Context, site string, fn func() error) error
}

// RegistryHealth is the readiness probe contract for the registry
// backend. *valkey.Store satisfies this; handler tests substitute a
// fake that returns the desired error.
type RegistryHealth interface {
	Ping(ctx context.Context) error
}

type PGHealth interface {
	Ping(ctx context.Context) error
}

// Handlers carries the dependencies needed by every endpoint in this package.
type Handlers struct {
	GH                 GitHubAuthenticator
	JWT                DeployJWTSigner
	Sites              SitesProvider
	Registry           RegistryWriter
	Health             RegistryHealth
	PGHealth           PGHealth
	R2                 R2Store
	AliasProductionFmt string // e.g. "<site>/production"
	AliasPreviewFmt    string // e.g. "<site>/preview"
	Tombstones         TombstoneStore
	TrashPrefixBase    string // e.g. "_trash/"
	Trash              TrashStore
	TrashRecovery      time.Duration
	Outbox             SiteChangeEmitter
	Index              DeployIndexWriter
	Locker             SiteLocker
	Audit              AuditStore
	// DeployPrefix is the parsed deploy-key template.
	DeployPrefix DeployPrefixTemplate
	// UploadMaxBytes caps a single PUT /upload body size. 0 or
	// negative means uncapped — production wiring sets a finite default
	// (UPLOAD_MAX_BYTES env, 100 MiB by default).
	UploadMaxBytes int64
	// RegistryAuthzTeam gates state-mutating /api/site/* endpoints
	// (register/update/delete). Caller must be on this team. Default
	// "staff" via config; production wiring sets it from
	// REGISTRY_AUTHZ_TEAM env.
	RegistryAuthzTeam string
	// Repo* drive the /api/repo* endpoints. RepoGH probes team
	// membership in the Universe org (distinct from GH, which is scoped
	// to GitHubConfig.Org) — see dossier §V4. Repos is the request
	// queue; GitHubApp mints the Apollo-11 token + creates repos. These
	// are nil when the feature is disabled (routes left unmounted).
	RepoGH               GitHubAuthenticator
	Repos                RepoStore
	GitHubApp            RepoCreator
	RepoOrg              string
	RepoCreateAuthzTeam  string
	RepoApproveAuthzTeam string
	NewDeployID          func(sha string) string
	Now                  func() time.Time
	PublicURLForSite     func(site, mode string) string // e.g. preview → "https://www.preview.freecode.camp"

	readyzValkey probeState
	readyzR2     probeState
}

var errAliasWriteHandled = errors.New("alias write failure already written to response")

func (h *Handlers) withSiteLock(ctx context.Context, dirname string, fn func() error) error {
	if h.Locker == nil {
		return fn()
	}
	return h.Locker.WithSiteLock(ctx, dirname, fn)
}

func (h *Handlers) emitSiteChanged(ctx context.Context, site string) {
	if h.Outbox == nil {
		return
	}
	site = h.DeployPrefix.SiteDirname(site)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.Outbox.EnqueueSiteChanged(ctx, site); err != nil {
		slog.ErrorContext(ctx, "outbox.enqueue.failed", "site", site, "err", err)
		sentry.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("op", "outbox.enqueue")
			scope.SetTag("site", site)
			scope.SetFingerprint([]string{"outbox.enqueue"})
			sentry.CaptureException(err)
		})
	}
}

func (h *Handlers) auditFromScope(ctx context.Context, action, outcome string, detail map[string]any) {
	sc := telemetry.FromContext(ctx)
	h.audit(ctx, pg.AuditEvent{
		Actor:     sc.Actor(),
		Action:    action,
		Site:      sc.Site(),
		DeployID:  sc.DeployID(),
		Outcome:   outcome,
		RequestID: sc.ReqID,
		Detail:    detail,
	})
}

func (h *Handlers) audit(ctx context.Context, e pg.AuditEvent) {
	if h.Audit == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.Audit.RecordAudit(auditCtx, e); err != nil {
		slog.ErrorContext(ctx, "audit.write.failed", "action", e.Action, "err", err)
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("op", "audit.record")
				scope.SetFingerprint([]string{"audit.record"})
				hub.CaptureException(err)
			})
		}
		return
	}
}

func (h *Handlers) logAction(ctx context.Context, action, outcome string, attrs ...slog.Attr) {
	sc := telemetry.FromContext(ctx)
	sc.SetAction(action)
	sc.SetOutcome(outcome)
	slog.LogAttrs(ctx, slog.LevelInfo, action, attrs...)
}

func writeGitHubProbeError(w http.ResponseWriter, err error) {
	if auth.IsGitHubRateLimited(err) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "github api rate limited; retry later")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
}

// writeJSON marshals v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	if sw, ok := w.(*statusWriter); ok {
		sw.errCode = code
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// writeUpstreamError logs err with full context and writes an opaque
// generic message to the client. Use whenever err comes from a
// transitive dependency (R2 SDK, go-redis, GitHub API) whose strings
// may leak internal endpoints, bucket names, or storage keys. `op` is
// a short filterable label for the failing operation (e.g.,
// "r2.put.alias", "valkey.register").
func writeUpstreamError(w http.ResponseWriter, r *http.Request, status int, code, op string, err error) {
	slog.ErrorContext(r.Context(), "upstream.error",
		"op", op,
		"err", err,
		"path", r.URL.Path,
	)
	reportUpstream(r, code, op, err)
	writeError(w, status, code, "upstream call failed")
}

func reportUpstream(r *http.Request, code, op string, err error) {
	if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
		sc := telemetry.FromContext(r.Context())
		hub.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("op", op)
			scope.SetTag("error_code", code)
			if site := sc.Site(); site != "" {
				scope.SetTag("site", site)
			}
			if deployID := sc.DeployID(); deployID != "" {
				scope.SetTag("deployId", deployID)
			}
			scope.SetFingerprint([]string{"upstream", op})
			hub.CaptureException(err)
		})
	}
}

func writeLockError(w http.ResponseWriter, r *http.Request, err error) {
	if pg.IsLockTimeout(err) {
		slog.WarnContext(r.Context(), "site.lock.contended",
			"op", "pg.lock.site",
			"path", r.URL.Path,
		)
		writeError(w, http.StatusConflict, "site_locked", "another operation on this site is in progress; retry shortly")
		return
	}
	writeUpstreamError(w, r, http.StatusBadGateway, "site_lock_failed", "pg.lock.site", err)
}

// errBadRequest is a sentinel for malformed bodies.
var errBadRequest = errors.New("bad request")

const (
	maxJSONBodyBytes     = 64 << 10
	maxManifestBodyBytes = 8 << 20
)

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return false
	}
	return true
}

func decodeJSONOptional(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return false
	}
	return true
}
