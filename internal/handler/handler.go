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
	"github.com/freeCodeCamp/artemis/internal/registry"
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
}

// RegistryHealth is the readiness probe contract for the registry
// backend. *valkey.Store satisfies this; handler tests substitute a
// fake that returns the desired error.
type RegistryHealth interface {
	Ping(ctx context.Context) error
}

// Handlers carries the dependencies needed by every endpoint in this package.
type Handlers struct {
	GH                 GitHubAuthenticator
	JWT                DeployJWTSigner
	Sites              SitesProvider
	Registry           RegistryWriter
	Health             RegistryHealth
	R2                 R2Store
	AliasProductionFmt string // e.g. "<site>/production"
	AliasPreviewFmt    string // e.g. "<site>/preview"
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
	// Metrics, if non-nil, drives the per-endpoint counters surfaced
	// at /metrics. SitePromote / SiteRollback use h.Metrics directly;
	// writeUpstreamError reaches for the package-level handle installed
	// via SetMetrics.
	Metrics *Metrics
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
	slog.Error("upstream error",
		"op", op,
		"err", err,
		"reqID", RequestIDFromContext(r.Context()),
		"path", r.URL.Path,
	)
	if pkgMetrics != nil {
		pkgMetrics.UpstreamErrors.WithLabelValues(op).Inc()
	}
	// Capture as a Sentry issue grouped by op, so r2.put.upload failures
	// cluster apart from valkey.register etc. The raw err goes to Sentry
	// (internal, access-controlled) even though the client sees only the
	// opaque message; request headers carrying tokens are scrubbed in
	// observability.scrubEvent before delivery.
	if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
		hub.WithScope(func(scope *sentry.Scope) {
			scope.SetTag("op", op)
			scope.SetTag("error_code", code)
			scope.SetFingerprint([]string{"upstream", op})
			hub.CaptureException(err)
		})
	}
	writeError(w, status, code, "upstream call failed")
}

// errBadRequest is a sentinel for malformed bodies.
var errBadRequest = errors.New("bad request")
