package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/reporequest"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimal stub handlers — we only assert routing wiring here; behaviour is
// covered by the handler package tests.
type stubHandlers struct {
	*handler.Handlers
}

func TestRouter_HealthzNoAuth(t *testing.T) {
	r := New(&handler.Handlers{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"ok":true}`, w.Body.String())
}

func TestRouter_WhoamiRequiresBearer(t *testing.T) {
	r := New(&handler.Handlers{})

	req := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRouter_UploadRequiresJWT(t *testing.T) {
	r := New(&handler.Handlers{})

	req := httptest.NewRequest(http.MethodPut, "/api/deploy/d1/upload?path=index.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRouter_UnknownRoute_404(t *testing.T) {
	r := New(&handler.Handlers{})

	req := httptest.NewRequest(http.MethodGet, "/api/nonsense", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_RequestIDHeaderEcho(t *testing.T) {
	r := New(&handler.Handlers{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "trace-7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "trace-7", w.Header().Get("X-Request-ID"))
}

var _ = stubHandlers{}

// --- repo feature route-mount wiring ---

type stubRepoStore struct{}

func (stubRepoStore) Create(context.Context, reporequest.Request) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) Get(context.Context, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) List(context.Context) ([]reporequest.Request, error) { return nil, nil }
func (stubRepoStore) Approve(context.Context, string, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) Reject(context.Context, string, string, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) MarkActive(context.Context, string, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) MarkFailed(context.Context, string, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) MarkStale(context.Context, string, string) (reporequest.Request, error) {
	return reporequest.Request{}, nil
}
func (stubRepoStore) Delete(context.Context, string) error { return nil }

type stubRepoCreator struct{}

func (stubRepoCreator) CreateRepo(context.Context, githubapp.CreateSpec) (githubapp.Created, error) {
	return githubapp.Created{}, nil
}
func (stubRepoCreator) ListTemplates(context.Context) ([]string, error) { return nil, nil }
func (stubRepoCreator) RepoExists(context.Context, string) (bool, string, error) {
	return false, "", nil
}

type stubRepoGH struct{}

func (stubRepoGH) ValidateToken(context.Context, string) (string, error) { return "", nil }
func (stubRepoGH) AuthorizeForSite(context.Context, string, string, []string) (bool, error) {
	return false, nil
}
func (stubRepoGH) UserTeams(context.Context, string) ([]string, error) { return nil, nil }

func TestRouter_RepoRoutesUnmountedByDefault(t *testing.T) {
	r := New(&handler.Handlers{})
	req := httptest.NewRequest(http.MethodPost, "/api/repo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "repo routes must be absent when feature disabled")
}

func TestRouter_RepoRoutesMountedWhenEnabled(t *testing.T) {
	h := &handler.Handlers{
		Repos:     stubRepoStore{},
		GitHubApp: stubRepoCreator{},
		RepoGH:    stubRepoGH{},
	}
	r := New(h)
	// Route present but unauthenticated → bearer middleware returns 401.
	req := httptest.NewRequest(http.MethodPost, "/api/repo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "repo route must be mounted (bearer-guarded) when feature enabled")
}

type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (c *captureTransport) Configure(sentry.ClientOptions) {}
func (c *captureTransport) SendEvent(e *sentry.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}
func (c *captureTransport) Flush(time.Duration) bool              { return true }
func (c *captureTransport) FlushWithContext(context.Context) bool { return true }
func (c *captureTransport) Close()                                {}

func TestRouter_SentryMiddlewareMountedWhenClientConfigured(t *testing.T) {
	tr := &captureTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.test/1",
		Transport:        tr,
		EnableTracing:    true,
		TracesSampleRate: 1.0,
	})
	require.NoError(t, err)

	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(client)
	t.Cleanup(func() { hub.BindClient(prev) })

	r := New(&handler.Handlers{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	client.Flush(2 * time.Second)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	var names []string
	for _, e := range tr.events {
		if e.Type == "transaction" {
			names = append(names, e.Transaction)
		}
	}
	require.NotEmpty(t, names, "sentryhttp middleware mounted so a transaction was emitted; total events=%d", len(tr.events))
	assert.Contains(t, names, "GET /healthz", "retagTransaction set the tx name to the chi route pattern")
}
