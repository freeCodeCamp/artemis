package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
)

// Per-key unexported struct{} types are the idiomatic Go pattern for
// context keys (see Go Code Review Comments §"Contexts"): zero
// allocation, type-safe, and impossible to collide across packages.
type (
	loginCtxKey     struct{}
	jwtClaimsCtxKey struct{}
	ghTokenCtxKey   struct{}
	reqIDCtxKey     struct{}
)

var (
	ctxKeyLogin = loginCtxKey{}
	ctxKeyJWT   = jwtClaimsCtxKey{}
	ctxKeyToken = ghTokenCtxKey{}
	ctxKeyReqID = reqIDCtxKey{}
)

// LoginFromContext returns the GitHub login resolved by the GitHub-bearer
// middleware, or "" if not present.
func LoginFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyLogin).(string)
	return v
}

// GitHubTokenFromContext returns the raw GitHub bearer token attached by
// the GitHub-bearer middleware. Used by handlers that need to do further
// per-request team-membership probes.
func GitHubTokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyToken).(string)
	return v
}

// JWTClaimsFromContext returns the deploy-session JWT claims resolved by
// the deploy-JWT middleware, or zero value if not present.
func JWTClaimsFromContext(ctx context.Context) (auth.DeploySessionClaims, bool) {
	v, ok := ctx.Value(ctxKeyJWT).(auth.DeploySessionClaims)
	return v, ok
}

// RequestIDFromContext returns the per-request id assigned by RequestID
// middleware.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyReqID).(string)
	return v
}

// extractBearer returns the token portion of an "Authorization: Bearer X"
// header, or "" if missing/malformed.
//
// RFC 6750 strict: exactly one space between "Bearer" and the token,
// no leading/trailing/internal whitespace in the token.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	tok := h[len(prefix):]
	if tok == "" {
		return ""
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return ""
	}
	return tok
}

// RequireGitHubBearer validates an incoming GitHub PAT/OIDC token via
// the GH client and attaches the resolved login to the request context.
//
// Status mapping:
//
//	missing/malformed Authorization → 401
//	GH /user 401/403 (non-rate-limit) → 401
//	GH rate-limited                  → 429
//	GH 5xx                           → 503
func (h *Handlers) RequireGitHubBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing_bearer", "missing or malformed Authorization header")
			return
		}
		login, err := h.GH.ValidateToken(r.Context(), token)
		if err == nil {
			telemetry.FromContext(r.Context()).SetActor(login)
		}
		if err != nil {
			switch {
			case auth.IsGitHubRateLimited(err):
				writeError(w, http.StatusTooManyRequests, "rate_limited", "github api rate limited; retry later")
			case auth.IsGitHubUnavailable(err):
				writeError(w, http.StatusServiceUnavailable, "upstream_unavailable", "github api unavailable; retry later")
			case auth.IsGitHubUnauthenticated(err):
				writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid github token")
			default:
				writeError(w, http.StatusUnauthorized, "unauthenticated", "could not validate github token")
			}
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyLogin, login)
		ctx = context.WithValue(ctx, ctxKeyToken, token)
		// Identify the caller on the request-scoped Sentry hub by GitHub
		// login only — never the bearer token. SendDefaultPII is off, so
		// no IP/headers are attached automatically.
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.Scope().SetUser(sentry.User{Username: login})
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireDeployJWT validates the deploy-session JWT and attaches its
// claims to the request context.
//
// Status mapping:
//
//	missing/malformed → 401
//	expired           → 401
//	any other parse/verify failure → 403
func (h *Handlers) RequireDeployJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing_bearer", "missing or malformed Authorization header")
			return
		}
		claims, err := h.JWT.Verify(token)
		if err != nil {
			if auth.IsExpired(err) {
				writeError(w, http.StatusUnauthorized, "jwt_expired", "deploy-session jwt expired")
				return
			}
			writeError(w, http.StatusForbidden, "jwt_invalid", "invalid deploy-session jwt")
			return
		}
		if len(h.Sites.Snapshot().TeamsForSite(claims.Site)) == 0 {
			writeError(w, http.StatusForbidden, "site_unauthorized", "site is not registered or has no authorized teams")
			return
		}
		telemetry.FromContext(r.Context()).SetActor(claims.Subject)
		ctx := context.WithValue(r.Context(), ctxKeyJWT, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestID assigns a per-request id, exposes it on the response header
// `X-Request-ID`, and stashes it in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var b [12]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyReqID, id)
		ctx = telemetry.NewContext(ctx, telemetry.New(id))
		// Tag the per-request Sentry hub so every event/transaction is
		// filterable by request id — the join key across Sentry, the
		// stdout logs, and the X-Request-ID response header.
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			hub.Scope().SetTag("request_id", id)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recoverer converts panics into 500 responses with a logged stack trace.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Capture to the request-scoped Sentry hub with a full
				// stacktrace before we swallow the panic into a 500.
				if hub := sentry.GetHubFromContext(r.Context()); hub != nil {
					hub.RecoverWithContext(r.Context(), rec)
				}
				slog.Error("panic in handler",
					"path", r.URL.Path,
					"reqID", RequestIDFromContext(r.Context()),
					"panic", rec,
					"stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLogSkipPaths are request URIs that bypass AccessLog. Health
// + readiness + metrics probes from k8s arrive every few seconds
// and flood the log without operator value; status codes feed the
// regular probe-result counters instead.
var accessLogSkipPaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// AccessLog emits one structured log line per request after the handler
// returns. Probe paths in accessLogSkipPaths are silenced.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		if _, skip := accessLogSkipPaths[r.URL.Path]; skip {
			return
		}
		sc := telemetry.FromContext(r.Context())
		args := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.code,
			"durMS", time.Since(start).Milliseconds(),
			"reqID", sc.ReqID,
			"login", sc.Actor(),
			"actor", sc.Actor(),
		}
		if sw.code >= 400 && sw.errCode != "" {
			args = append(args, "errCode", sw.errCode)
		}
		slog.Info("http", args...)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code    int
	errCode string
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
