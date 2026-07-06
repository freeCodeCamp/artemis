package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

func (h *capturingHandler) httpAttr(t *testing.T, key string) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != "http" {
			continue
		}
		var out string
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				out = a.Value.String()
				return false
			}
			return true
		})
		return out
	}
	t.Fatalf("no http access-log record captured")
	return ""
}

func (h *capturingHandler) findAction(action, outcome string) (map[string]string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != action {
			continue
		}
		m := map[string]string{}
		rec.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value.String()
			return true
		})
		if outcome == "" || m["outcome"] == outcome {
			return m, true
		}
	}
	return nil, false
}

func (h *capturingHandler) httpKeyCount(t *testing.T, key string) int {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rec := range h.records {
		if rec.Message != "http" {
			continue
		}
		n := 0
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == key {
				n++
			}
			return true
		})
		return n
	}
	t.Fatalf("no http access-log record captured")
	return 0
}

func captureAccessLog(t *testing.T) *capturingHandler {
	t.Helper()
	cap := &capturingHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewLogHandler(cap)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return cap
}

func TestAccessLog_GitHubBearer_ActorPopulated(t *testing.T) {
	cap := captureAccessLog(t)
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{"good": "alice"}},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := RequestID(AccessLog(h.RequireGitHubBearer(final)))

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer good")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "alice", cap.httpAttr(t, "actor"))
}

func TestAccessLog_NoDuplicateKeys(t *testing.T) {
	cap := captureAccessLog(t)
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{"good": "alice"}},
		&fakeSites{bySite: map[string][]string{}},
		newFakeR2())

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := RequestID(AccessLog(h.RequireGitHubBearer(final)))

	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("Authorization", "Bearer good")
	chain.ServeHTTP(httptest.NewRecorder(), r)

	assert.Equal(t, 0, cap.httpKeyCount(t, "login"), "login dropped (dup of actor)")
	assert.Equal(t, 0, cap.httpKeyCount(t, "reqID"), "reqID replaced by request_id")
	assert.Equal(t, 1, cap.httpKeyCount(t, "actor"), "exactly one actor key")
	assert.Equal(t, 1, cap.httpKeyCount(t, "request_id"), "exactly one request_id key")
}

func TestAccessLog_DeployJWT_ActorPopulated(t *testing.T) {
	cap := captureAccessLog(t)
	h, jwt := newTestHandlers(t,
		&fakeGH{},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		newFakeR2())

	tok, _, err := jwt.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := RequestID(AccessLog(h.RequireDeployJWT(final)))

	r := httptest.NewRequest(http.MethodPut, "/api/deploy/d-1/upload", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "alice", cap.httpAttr(t, "actor"))
}
