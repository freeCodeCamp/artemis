package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

// SiteRegisterRequest is the body of POST /api/site/register.
type SiteRegisterRequest struct {
	Slug  string   `json:"slug"`
	Teams []string `json:"teams,omitempty"`
}

// SiteRegisterResponse is the 201 body returned on successful
// registration. It mirrors the row shape callers see from
// GET /api/sites for envelope symmetry.
type SiteRegisterResponse struct {
	Slug      string    `json:"slug"`
	Teams     []string  `json:"teams"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedBy string    `json:"createdBy"`
}

// slugRe matches DNS-safe site slugs: 1-63 chars, lowercase letter
// first, then lowercase letters / digits / hyphens. Mirrors the
// `<site>.freecode.camp` constraint — slugs become subdomains.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// teamSlugRe matches GitHub team slugs: 1-39 chars, lowercase letter
// or digit first, then lowercase letters / digits / hyphens / underscores.
var teamSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,38}$`)

// SiteRegister implements POST /api/site/register — creates a new
// site row in the registry. Authz: caller must be on
// h.RegistryAuthzTeam (default "staff"). On empty/missing teams field
// the handler defaults to [h.RegistryAuthzTeam].
//
// Status matrix:
//
//	201 Created         — registered; body = SiteRegisterResponse
//	400 Bad Request     — invalid slug / team format / json
//	403 Forbidden       — caller not in the authz team
//	409 Conflict        — slug already registered
//	502 Bad Gateway     — registry write failed
//	503 Service Unavail — github membership probe upstream error
func (h *Handlers) SiteRegister(w http.ResponseWriter, r *http.Request) {
	if err := h.requireRegistryAuthz(w, r); err != nil {
		return
	}

	var req SiteRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json body")
		return
	}
	if !slugRe.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"slug must be 1-63 chars, lowercase letter first, then [a-z0-9-]")
		return
	}

	teams := req.Teams
	if len(teams) == 0 {
		teams = []string{h.RegistryAuthzTeam}
	}
	for _, t := range teams {
		if !teamSlugRe.MatchString(t) {
			writeError(w, http.StatusBadRequest, "invalid_team",
				"team slugs must be 1-39 chars matching [a-z0-9][a-z0-9_-]*")
			return
		}
	}

	login := LoginFromContext(r.Context())
	site, err := h.Registry.Register(r.Context(), req.Slug, teams, login)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "already_exists", "site is already registered")
		default:
			writeError(w, http.StatusBadGateway, "registry_write_failed", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusCreated, SiteRegisterResponse{
		Slug:      site.Slug,
		Teams:     site.Teams,
		CreatedAt: site.CreatedAt,
		UpdatedAt: site.UpdatedAt,
		CreatedBy: site.CreatedBy,
	})
}

// requireRegistryAuthz enforces that the authenticated caller is on
// h.RegistryAuthzTeam. Writes the response on failure and returns a
// non-nil error so the caller can early-return.
func (h *Handlers) requireRegistryAuthz(w http.ResponseWriter, r *http.Request) error {
	if h.RegistryAuthzTeam == "" {
		writeError(w, http.StatusInternalServerError, "misconfigured", "RegistryAuthzTeam not set")
		return errBadRequest
	}
	login := LoginFromContext(r.Context())
	token := GitHubTokenFromContext(r.Context())
	ok, err := h.GH.AuthorizeForSite(r.Context(), token, login, []string{h.RegistryAuthzTeam})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "could not probe team membership")
		return err
	}
	if !ok {
		writeError(w, http.StatusForbidden, "user_unauthorized",
			"caller is not on the registry-authz team")
		return errBadRequest
	}
	return nil
}
