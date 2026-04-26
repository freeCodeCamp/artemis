package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireGitHubBearer_MissingHeader(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{}},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	w := httptest.NewRecorder()
	h.RequireGitHubBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler must not run on missing bearer")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireGitHubBearer_RateLimited_Returns429(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{upstreamErr: auth.ErrGitHubRateLimited},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	h.RequireGitHubBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not pass on rate limit")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

func TestRequireGitHubBearer_5xx_Returns503(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{upstreamErr: auth.ErrGitHubUnavailable},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer x")
	w := httptest.NewRecorder()
	h.RequireGitHubBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRequireGitHubBearer_OK_AttachesLoginToContext(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{"good": "alice"}},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	var seen string
	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer good")
	w := httptest.NewRecorder()
	h.RequireGitHubBearer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = LoginFromContext(r.Context())
	})).ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "alice", seen)
}

func TestRequireDeployJWT_MissingHeader(t *testing.T) {
	h, _ := newTestHandlers(t, &fakeGH{}, &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	r := httptest.NewRequest(http.MethodPut, "/api/deploy/d1/upload", nil)
	w := httptest.NewRecorder()
	h.RequireDeployJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not pass on missing jwt")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireDeployJWT_BadToken_Returns403(t *testing.T) {
	h, _ := newTestHandlers(t, &fakeGH{}, &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	r := httptest.NewRequest(http.MethodPut, "/api/deploy/d1/upload", nil)
	r.Header.Set("Authorization", "Bearer not-a-jwt")
	w := httptest.NewRecorder()
	h.RequireDeployJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not pass on bogus jwt")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestRequireDeployJWT_OK_AttachesClaims(t *testing.T) {
	h, jwt := newTestHandlers(t, &fakeGH{}, &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	tok, _, err := jwt.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	var sawDeploy string
	r := httptest.NewRequest(http.MethodPut, "/api/deploy/d-1/upload", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.RequireDeployJWT(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		c, _ := JWTClaimsFromContext(r.Context())
		sawDeploy = c.DeployID
	})).ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "d-1", sawDeploy)
}

func TestRequestID_AddsHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, RequestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, r)

	assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
}

func TestRequestID_RespectsIncoming(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("X-Request-ID", "req-abc")
	w := httptest.NewRecorder()

	RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "req-abc", RequestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, r)

	assert.Equal(t, "req-abc", w.Header().Get("X-Request-ID"))
}

func TestRecoverer_TurnsPanicInto500(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAccessLog_PassesThroughStatus(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	AccessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewing"))
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusTeapot, w.Code)
	assert.Equal(t, "brewing", w.Body.String())
}

func TestRequireGitHubBearer_BadToken_Returns401(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{}},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer bogus")
	w := httptest.NewRecorder()
	h.RequireGitHubBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not pass on bogus token")
	})).ServeHTTP(w, r)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestExtractBearer_MalformedHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	assert.Equal(t, "", extractBearer(r))
	r.Header.Set("Authorization", "Basic abcd")
	assert.Equal(t, "", extractBearer(r))
	r.Header.Set("Authorization", "Bearer  trimmed-spaces")
	assert.Equal(t, "trimmed-spaces", extractBearer(r))
}
