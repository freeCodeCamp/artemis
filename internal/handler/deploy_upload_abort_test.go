package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestDeployUpload_ClientAbort_NoUpstreamError(t *testing.T) {
	store := newFakeR2()
	store.putErr = fmt.Errorf("r2 put: operation error S3: PutObject: %w", context.Canceled)
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		[]byte("<h1>hi</h1>"),
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, statusClientClosedRequest, w.Code,
		"client abort must take the shared 499 path, not a silent 200")
	require.Contains(t, w.Body.String(), "client_closed_request")
	require.NotContains(t, w.Body.String(), "upstream call failed")
}

func TestDeployUpload_ClientAbort_AccessLoggedAs499(t *testing.T) {
	cap := captureAccessLog(t)

	store := newFakeR2()
	store.putErr = fmt.Errorf("r2 put: operation error S3: PutObject: %w", context.Canceled)
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	router := chi.NewRouter()
	router.Method(http.MethodPut, "/api/deploy/{deployId}/upload",
		h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload)))

	r := httptest.NewRequest(http.MethodPut,
		"/api/deploy/"+deployID+"/upload?path=index.html",
		strings.NewReader("<h1>hi</h1>"))
	r.Header.Set("Authorization", "Bearer "+tok)
	AccessLog(router).ServeHTTP(httptest.NewRecorder(), r)

	require.Equal(t, "499", cap.httpAttr(t, "status"),
		"an aborted upload must not be access-logged as the statusWriter default 200")
	require.Equal(t, "client_closed_request", cap.httpAttr(t, "errCode"))
	require.Equal(t, 1, cap.countMessage("client.disconnect"))
	require.Equal(t, slog.LevelWarn, cap.levelOf(t, "client.disconnect"))
	require.Zero(t, cap.countMessage("deploy.upload.canceled"),
		"the v1.6.0 upload-only cancel branch is gone")
	require.Zero(t, cap.countMessage("upstream.error"))
}
