// Package handler implements the HTTP endpoints defined in ADR-016 §API surface.
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
	"github.com/freeCodeCamp/artemis/internal/sites"
)

// GitHubAuthenticator is the subset of *auth.GitHubClient used by the
// handler layer.
type GitHubAuthenticator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
	AuthorizeForSite(ctx context.Context, token, login string, teams []string) (bool, error)
	// UserTeams returns the slugs of every team in the configured org
	// that `token` is a member of. One paginated call replaces N×M
	// per-site IsTeamMember probes in WhoAmI (B9).
	UserTeams(ctx context.Context, token string) ([]string, error)
}

// DeployJWTSigner is the subset of *auth.DeploySessionSigner used by the
// handler layer.
type DeployJWTSigner interface {
	Sign(login, site, deployID string) (string, time.Time, error)
	Verify(token string) (auth.DeploySessionClaims, error)
}

// SitesProvider is the subset of *sites.Loader used here.
type SitesProvider interface {
	Snapshot() sites.Snapshot
}

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
	R2                 R2Store
	AliasProductionFmt string // e.g. "<site>/production"
	AliasPreviewFmt    string // e.g. "<site>/preview"
	// DeployPrefix is the parsed deploy-key template. Replaces the
	// raw string + brittle stripDeployIDFromFmt surgery (B7).
	DeployPrefix DeployPrefixTemplate
	// UploadMaxBytes caps a single PUT /upload body size (B4). 0 or
	// negative means uncapped — production wiring sets a finite default
	// (UPLOAD_MAX_BYTES env, 100 MiB by default).
	UploadMaxBytes   int64
	NewDeployID      func(sha string) string
	Now              func() time.Time
	PublicURLForSite func(site, mode string) string // e.g. preview → "https://www.preview.freecode.camp"
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
