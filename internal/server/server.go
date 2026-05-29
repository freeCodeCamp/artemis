// Package server wires the Handlers + middleware into a chi router.
//
// Route table:
//
//	GET    /healthz                                       — no auth (liveness)
//	GET    /readyz                                        — no auth (readiness; probes Valkey + R2)
//	GET    /metrics                                       — no auth (prometheus exposition)
//	GET    /api/whoami                                    — GitHub bearer
//	POST   /api/deploy/init                               — GitHub bearer
//	PUT    /api/deploy/{deployId}/upload                  — Deploy-session JWT
//	POST   /api/deploy/{deployId}/finalize                — Deploy-session JWT
//	GET    /api/sites                                     — GitHub bearer
//	POST   /api/site/register                             — GitHub bearer + registry-authz team
//	PATCH  /api/site/{slug}                               — GitHub bearer + registry-authz team
//	DELETE /api/site/{slug}                               — GitHub bearer + registry-authz team
//	GET    /api/site/{site}/deploys                       — GitHub bearer
//	GET    /api/site/{site}/alias/{mode}                  — GitHub bearer
//	POST   /api/site/{site}/promote                       — GitHub bearer
//	POST   /api/site/{site}/rollback                      — GitHub bearer
//	POST   /api/repo                                      — GitHub bearer + repo-create team   (feature-gated)
//	GET    /api/repos                                     — GitHub bearer                       (feature-gated)
//	GET    /api/repo/templates                            — GitHub bearer                       (feature-gated)
//	GET    /api/repo/{id}                                 — GitHub bearer                       (feature-gated)
//	POST   /api/repo/{id}/approve                         — GitHub bearer + repo-approve team   (feature-gated)
//	POST   /api/repo/{id}/reject                          — GitHub bearer + repo-approve team   (feature-gated)
package server

import (
	"net/http"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// New returns a chi router fully wired with the Handlers' endpoints +
// the standard middleware chain (RequestID → AccessLog → Recoverer).
// metricsGatherer, when non-nil, is mounted at /metrics; pass nil to
// disable the endpoint (useful for tests that don't care).
func New(h *handler.Handlers, metricsGatherer prometheus.Gatherer) http.Handler {
	r := chi.NewRouter()
	r.Use(handler.RequestID)
	r.Use(handler.AccessLog)
	r.Use(handler.Recoverer)

	// Public.
	r.Get("/healthz", h.HealthZ)
	r.Get("/readyz", h.ReadyZ)
	if metricsGatherer != nil {
		r.Method(http.MethodGet, "/metrics", handler.MetricsHandler(metricsGatherer))
	}

	// /api/* — GitHub bearer required for the human-driven endpoints.
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(h.RequireGitHubBearer)
			r.Get("/whoami", h.WhoAmI)
			r.Post("/deploy/init", h.DeployInit)
			r.Get("/sites", h.SitesList)
			r.Post("/site/register", h.SiteRegister)
			r.Patch("/site/{slug}", h.SiteUpdate)
			r.Delete("/site/{slug}", h.SiteDelete)
			r.Get("/site/{site}/deploys", h.SiteDeploys)
			r.Get("/site/{site}/alias/{mode}", h.AliasGet)
			r.Post("/site/{site}/promote", h.SitePromote)
			r.Post("/site/{site}/rollback", h.SiteRollback)

			// Repo-creation feature — mounted only when the Apollo-11
			// App credentials + queue store are wired (RepoEnabled).
			// Per-handler authz gates create (staff) vs approve (admin).
			if h.RepoEnabled() {
				r.Post("/repo", h.RepoCreate)
				r.Get("/repos", h.ReposList)
				r.Get("/repo/templates", h.RepoTemplates)
				r.Get("/repo/{id}", h.RepoGet)
				r.Post("/repo/{id}/approve", h.RepoApprove)
				r.Post("/repo/{id}/reject", h.RepoReject)
			}
		})

		// Deploy-session JWT branch — scoped to (login, site, deployId).
		r.Group(func(r chi.Router) {
			r.Use(h.RequireDeployJWT)
			r.Put("/deploy/{deployId}/upload", h.DeployUpload)
			r.Post("/deploy/{deployId}/finalize", h.DeployFinalize)
		})
	})

	return r
}
