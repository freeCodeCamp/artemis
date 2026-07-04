package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

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

	require.NotEqual(t, http.StatusBadGateway, w.Code,
		"client abort must not route through writeUpstreamError")
	require.NotContains(t, w.Body.String(), "upstream call failed")
}
