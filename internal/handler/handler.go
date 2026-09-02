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
	"github.com/freeCodeCamp/artemis/internal/sitekey"
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
	Sign(login string, site sitekey.Slug, deployID string) (string, time.Time, error)
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
	DeleteAlias(ctx context.Context, aliasKey string) error
	GetAlias(ctx context.Context, aliasKey string) (string, error)
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	HasPrefix(ctx context.Context, prefix string) (bool, error)
	HasObject(ctx context.Context, key string) (bool, error)
	Ping(ctx context.Context) error
	VerifyDeployComplete(ctx context.Context, prefix string, expected []string) error
	MovePrefix(ctx context.Context, src, dst string) (int, error)
	PrefixBytes(ctx context.Context, prefix string) (int64, error)
}

type DeployFenceStore interface {
	MarkDeployFinalized(ctx context.Context, site sitekey.Slug, deployID string, ttl time.Duration) error
	IsDeployFinalized(ctx context.Context, site sitekey.Slug, deployID string) (bool, error)
}

type TombstoneStore interface {
	RecordTombstone(ctx context.Context, site sitekey.Dirname, id string, bytes int64) error
	RecordSiteTombstone(ctx context.Context, site sitekey.Dirname) error
}

type AuditStore interface {
	RecordAudit(ctx context.Context, e pg.AuditEvent) error
	ListAudit(ctx context.Context, f pg.AuditFilter) ([]pg.AuditRecord, error)
	DeployActors(ctx context.Context, site string) (map[string]string, error)
}

type TrashStore interface {
	TombstonesForSite(ctx context.Context, site sitekey.Dirname) ([]gc.Tombstone, error)
	RestoreDeploy(ctx context.Context, site sitekey.Dirname, id string, mtime time.Time, bytes int64) error
}

type DeployIndexWriter interface {
	FinalizeAtomic(ctx context.Context, site sitekey.Dirname, deployID, mode string, mtime time.Time, bytes int64) error
	AliasAtomic(ctx context.Context, site sitekey.Dirname, name, deployID string, at time.Time) error
}

type PendingDeployWriter interface {
	BeginDeploy(ctx context.Context, site sitekey.Dirname, deployID string, mtime time.Time) error
}

// ReservationStore holds a deleted site's name for a grace period so
// the next claimant cannot inherit the previous owner's live bytes.
type ReservationStore interface {
	Reserve(ctx context.Context, slug sitekey.Slug, site sitekey.Dirname, until time.Time, by string, observed registry.ObservedAliases) (registry.Reservation, error)
}

// ReservationReverser restores a reserved name to its owner. The grace
// period promises a way back; without this it promises nothing.
type ReservationReverser interface {
	Reservation(ctx context.Context, slug sitekey.Slug) (registry.Reservation, error)
	Undelete(ctx context.Context, slug sitekey.Slug) (registry.Reservation, error)
}

// NameReleaser carries no deadline predicate, unlike the sweep's
// ReleaseReservation, which is why its caller is approver-gated.
type NameReleaser interface {
	ReleaseReservationNow(ctx context.Context, slug sitekey.Slug) error
	ExpireReservation(ctx context.Context, slug sitekey.Slug) error
}

type SiteLocker interface {
	WithSiteLock(ctx context.Context, site sitekey.Dirname, fn func(context.Context) error) error
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

	PublicProductionURLFmt string // e.g. "https://<site>.freecode.camp"
	PublicPreviewURLFmt    string // e.g. "https://<site>.preview.freecode.camp"
	EdgePurge              EdgePurger
	DeployFence            DeployFenceStore
	DeployJWTTTL           time.Duration
	Tombstones             TombstoneStore
	Reservations           ReservationStore
	NameReleaser           NameReleaser
	ReservationGrace       time.Duration
	TrashPrefixBase        string // e.g. "_trash/"
	Trash                  TrashStore
	TrashRecovery          time.Duration
	Index                  DeployIndexWriter
	Pending                PendingDeployWriter
	Locker                 SiteLocker
	Audit                  AuditStore
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
	AuditReadAuthzTeam   string
	NewDeployID          func(sha string) string
	Now                  func() time.Time

	readyzValkey probeState
	readyzR2     probeState
}

var errAliasWriteHandled = errors.New("alias write failure already written to response")

func (h *Handlers) withSiteLock(ctx context.Context, dirname sitekey.Dirname, fn func(context.Context) error) error {
	if h.Locker == nil {
		return fn(ctx)
	}
	var closureErr error
	var closureRan bool
	lockerErr := h.Locker.WithSiteLock(ctx, dirname, func(lockCtx context.Context) error {
		closureRan = true
		closureErr = fn(lockCtx)
		return closureErr
	})
	if closureRan {
		return closureErr
	}
	return lockerErr
}

func (h *Handlers) requireWritableSite(ctx context.Context, slug sitekey.Slug) (registry.Site, error) {
	site, err := h.Registry.GetSite(ctx, slug)
	if err != nil {
		return registry.Site{}, err
	}
	if site.IsReserved() {
		return site, registry.ErrReserved
	}
	return site, nil
}

// The cached snapshot drops reserved sites, so a name held after a
// delete reads there as one that never existed.
func (h *Handlers) denyUnregisteredSite(w http.ResponseWriter, r *http.Request, slug sitekey.Slug) string {
	site, err := h.requireWritableSite(r.Context(), slug)
	if errors.Is(err, registry.ErrReserved) {
		h.writeFenceError(w, r, "registry.get.authz", "", site, err)
		return "site_reserved"
	}
	writeError(w, http.StatusForbidden, "site_unauthorized", "site is not registered or has no authorized teams")
	return "site_unauthorized"
}

func (h *Handlers) writeFenceError(w http.ResponseWriter, r *http.Request, op, goneMessage string,
	site registry.Site, err error,
) {
	switch {
	case errors.Is(err, registry.ErrReserved):
		writeErrorDetail(w, http.StatusConflict, "site_reserved",
			"site name is reserved after a delete; undelete it or wait for the reclaim",
			map[string]any{"reservedUntil": site.ReservedUntil.UTC().Format(time.RFC3339)})
	case errors.Is(err, registry.ErrNotFound):
		writeError(w, http.StatusGone, "site_gone", goneMessage)
	default:
		writeUpstreamError(w, r, http.StatusBadGateway, "registry_read_failed", op, err)
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

const pendingWriteTimeout = 5 * time.Second

func (h *Handlers) beginPendingDeploy(ctx context.Context, site sitekey.Dirname, deployID string) {
	if h.Pending == nil {
		return
	}
	beginCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
	defer cancel()
	if err := h.Pending.BeginDeploy(beginCtx, site, deployID, h.Now().UTC()); err != nil {
		slog.ErrorContext(ctx, "deploy.pending.write_failed", "site", site, "deploy_id", deployID, "err", err)
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.WithScope(func(scope *sentry.Scope) {
				scope.SetTag("op", "deploy.pending")
				scope.SetFingerprint([]string{"deploy.pending"})
				hub.CaptureException(err)
			})
		}
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

func writeErrorDetail(w http.ResponseWriter, status int, code, message string, extra map[string]any) {
	if sw, ok := w.(*statusWriter); ok {
		sw.errCode = code
	}
	errObj := map[string]any{"code": code, "message": message}
	for k, v := range extra {
		errObj[k] = v
	}
	writeJSON(w, status, map[string]any{"error": errObj})
}

// writeUpstreamError logs err with full context and writes an opaque
// generic message to the client. Use whenever err comes from a
// transitive dependency (R2 SDK, go-redis, GitHub API) whose strings
// may leak internal endpoints, bucket names, or storage keys. `op` is
// a short filterable label for the failing operation (e.g.,
// "r2.put.alias", "valkey.register").
func writeUpstreamError(w http.ResponseWriter, r *http.Request, status int, code, op string, err error) {
	if errors.Is(err, context.Canceled) {
		slog.WarnContext(r.Context(), "client.disconnect",
			"op", op,
			"err", err,
			"path", r.URL.Path,
		)
		if r.Context().Err() == nil {
			writeError(w, statusClientClosedRequest, "client_closed_request", "request canceled by client")
			return
		}
		if sw, ok := w.(*statusWriter); ok {
			sw.wrote = true
			sw.code = statusClientClosedRequest
			sw.errCode = "client_closed_request"
		}
		return
	}
	slog.ErrorContext(r.Context(), "upstream.error",
		"op", op,
		"err", err,
		"path", r.URL.Path,
	)
	reportUpstream(r, code, op, err)
	writeError(w, status, code, "upstream call failed")
}

const statusClientClosedRequest = 499

func reportUpstream(r *http.Request, code, op string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
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
