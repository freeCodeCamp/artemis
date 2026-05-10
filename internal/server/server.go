// Package server wires the Handlers + middleware into a chi router.
//
// Route table (mirrors ADR-016 §API surface + RFC §B CLI surface):
//
//	GET    /healthz                                       — no auth
//	GET    /api/whoami                                    — GitHub bearer
//	POST   /api/deploy/init                               — GitHub bearer
//	PUT    /api/deploy/{deployId}/upload                  — Deploy-session JWT
//	POST   /api/deploy/{deployId}/finalize                — Deploy-session JWT
//	GET    /api/sites                                     — GitHub bearer
//	POST   /api/site/register                             — GitHub bearer + registry-authz team
//	PATCH  /api/site/{slug}                               — GitHub bearer + registry-authz team
//	GET    /api/site/{site}/deploys                       — GitHub bearer
//	POST   /api/site/{site}/promote                       — GitHub bearer
//	POST   /api/site/{site}/rollback                      — GitHub bearer
package server

import (
	"net/http"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/go-chi/chi/v5"
)

// New returns a chi router fully wired with the Handlers' endpoints +
// the standard middleware chain (RequestID → AccessLog → Recoverer).
func New(h *handler.Handlers) http.Handler {
	r := chi.NewRouter()
	r.Use(handler.RequestID)
	r.Use(handler.AccessLog)
	r.Use(handler.Recoverer)

	// Public.
	r.Get("/healthz", h.HealthZ)

	// /api/* — GitHub bearer required for the human-driven endpoints.
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(h.RequireGitHubBearer)
			r.Get("/whoami", h.WhoAmI)
			r.Post("/deploy/init", h.DeployInit)
			r.Get("/sites", h.SitesList)
			r.Post("/site/register", h.SiteRegister)
			r.Patch("/site/{slug}", h.SiteUpdate)
			r.Get("/site/{site}/deploys", h.SiteDeploys)
			r.Post("/site/{site}/promote", h.SitePromote)
			r.Post("/site/{site}/rollback", h.SiteRollback)
		})

		// Deploy-session JWT branch — narrowed scope per ADR-016 amendment.
		r.Group(func(r chi.Router) {
			r.Use(h.RequireDeployJWT)
			r.Put("/deploy/{deployId}/upload", h.DeployUpload)
			r.Post("/deploy/{deployId}/finalize", h.DeployFinalize)
		})
	})

	return r
}
