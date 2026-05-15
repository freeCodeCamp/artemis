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
	"net/http"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/registry"
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

// Handlers carries the dependencies needed by every endpoint in this package.
type Handlers struct {
	GH                 GitHubAuthenticator
	JWT                DeployJWTSigner
	Sites              SitesProvider
	Registry           RegistryWriter
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
	NewDeployID       func(sha string) string
	Now               func() time.Time
	PublicURLForSite  func(site, mode string) string // e.g. preview → "https://www.preview.freecode.camp"
}

// writeJSON marshals v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// errBadRequest is a sentinel for malformed bodies.
var errBadRequest = errors.New("bad request")
