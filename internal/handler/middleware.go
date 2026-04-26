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
)

type ctxKey string

const (
	ctxKeyLogin ctxKey = "login"
	ctxKeyJWT   ctxKey = "jwtClaims"
	ctxKeyToken ctxKey = "ghToken"
	ctxKeyReqID ctxKey = "reqID"
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
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recoverer converts panics into 500 responses with a logged stack trace.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
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

// AccessLog emits one structured log line per request after the handler
// returns.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.code,
			"durMS", time.Since(start).Milliseconds(),
			"reqID", RequestIDFromContext(r.Context()),
			"login", LoginFromContext(r.Context()),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
