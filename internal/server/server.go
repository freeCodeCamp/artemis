// Package server wires the Handlers + middleware into a chi router.
//
// Route table:
//
//	GET    /healthz                                       — no auth (liveness)
//	GET    /readyz                                        — no auth (readiness; probes Valkey + R2)
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
	"time"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const apiRequestTimeout = 60 * time.Second

// New returns a chi router fully wired with the Handlers' endpoints +
// the standard middleware chain (Sentry → RequestID → AccessLog →
// Recoverer).
func New(h *handler.Handlers) http.Handler {
	r := chi.NewRouter()
	// Mount the Sentry request middleware only when a client is actually
	// configured (Init ran with a DSN). When Sentry is disabled this adds
	// zero per-request overhead and the chain behaves exactly as before.
	// Outermost so the hub + tracing transaction wrap everything;
	// Repanic:false because the inner Recoverer owns panic->500 and
	// captures the panic to the hub itself.
	if sentry.CurrentHub().Client() != nil {
		r.Use(sentryhttp.New(sentryhttp.Options{Repanic: false}).Handle)
	}
	r.Use(handler.RequestID)
	r.Use(handler.AccessLog)
	r.Use(retagTransaction)
	r.Use(handler.Recoverer)

	// Public.
	r.Get("/healthz", h.HealthZ)
	r.Get("/readyz", h.ReadyZ)

	// /api/* — GitHub bearer required for the human-driven endpoints.
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(h.RequireGitHubBearer)
			r.Use(middleware.Timeout(apiRequestTimeout))
			r.Get("/whoami", h.WhoAmI)
			r.Post("/deploy/init", h.DeployInit)
			r.Get("/sites", h.SitesList)
			r.Post("/site/register", h.SiteRegister)
			r.Patch("/site/{slug}", h.SiteUpdate)
			r.Delete("/site/{slug}", h.SiteDelete)
			r.Get("/site/{site}/deploys", h.SiteDeploys)
			r.Delete("/site/{site}/deploys/{deployId}", h.SiteDeployDelete)
			r.Post("/site/{site}/deploys/{deployId}/restore", h.SiteDeployRestore)
			r.Get("/site/{site}/trash", h.SiteTrashList)
			r.Get("/site/{site}/alias/{mode}", h.AliasGet)
			r.Post("/site/{site}/promote", h.SitePromote)
			r.Post("/site/{site}/rollback", h.SiteRollback)
			r.Get("/audit", h.AuditList)

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
				r.Delete("/repo/{id}", h.RepoDelete)
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

func retagTransaction(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		tx := sentry.TransactionFromContext(r.Context())
		if tx == nil {
			return
		}
		rc := chi.RouteContext(r.Context())
		if rc == nil {
			return
		}
		if pattern := rc.RoutePattern(); pattern != "" {
			tx.Name = r.Method + " " + pattern
			tx.Source = sentry.SourceRoute
		}
	})
}
