package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withLogin(login, token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(contextWithLogin(r.Context(), login, token)))
		})
	}
}

func TestConflictBodies_CarryTheErrorCodeToTheAccessLog(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}

	t.Run("promote alias_drift keeps site and current beside the error", func(t *testing.T) {
		cap := captureAccessLog(t)
		store := newFakeR2()
		store.aliases["www/preview"] = "20260420-141522-newer1"
		store.aliases["www/production"] = "20260101-101010-current"
		h, _ := newTestHandlers(t, gh, standardSites(), store)

		router := chi.NewRouter()
		router.Use(RequestID, AccessLog, withLogin("alice", "tok"))
		router.Post("/api/site/{site}/promote", h.SitePromote)
		body, _ := json.Marshal(SitePromoteRequest{ExpectedCurrent: "20260101-101010-stale99"})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/site/www/promote", bytes.NewReader(body)))

		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		assert.Equal(t, "alias_drift", cap.httpAttr(t, "errCode"))
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "www", got["site"])
		assert.Equal(t, "20260101-101010-current", got["current"])
		assert.Equal(t, "alias_drift", got["error"].(map[string]any)["code"])
	})

	t.Run("finalize verify_failed keeps missing inside the error", func(t *testing.T) {
		cap := captureAccessLog(t)
		store := newFakeR2()
		deployID := "20260420-141522-abc1234"
		store.objects["www/deploys/"+deployID+"/index.html"] = []byte("<html>")
		h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
		tok, _, err := jwt.Sign("alice", "www", deployID)
		require.NoError(t, err)

		router := chi.NewRouter()
		router.Use(RequestID, AccessLog)
		router.Post("/api/deploy/{deployId}/finalize", h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP)
		body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html", "missing.js"}})
		req := httptest.NewRequest(http.MethodPost, "/api/deploy/"+deployID+"/finalize", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(context.Background()))

		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		assert.Equal(t, "verify_failed", cap.httpAttr(t, "errCode"))
		var got map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, []any{"missing.js"}, got["error"].(map[string]any)["missing"])
	})
}
