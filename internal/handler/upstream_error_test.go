package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpstreamError_DoesNotLeakUpstreamString asserts that when a
// transitive-dependency error reaches the handler, the upstream err
// string (which may include bucket names, endpoints, key paths) does
// NOT appear in the HTTP response body. The body must carry only the
// generic "upstream call failed" message + the stable code.
//
// We force an R2 list failure via the fake's listErr injection and
// drive SiteDeploys, which calls writeUpstreamError on failure.
func TestUpstreamError_DoesNotLeakUpstreamString(t *testing.T) {
	const sentinel = "secret-bucket-name.r2.cloudflarestorage.com/internal/path"

	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	store := newFakeR2()
	store.listErr = errors.New("r2 transport: GET https://" + sentinel + " EOF")

	h, _ := newTestHandlers(t, gh, standardSites(), store)

	w := withSiteRoute(http.MethodGet, "/api/site/{site}/deploys",
		"/api/site/www/deploys", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteDeploys,
	)
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	body := w.Body.String()
	assert.NotContains(t, body, sentinel,
		"response leaked upstream err string; writeUpstreamError contract violated")
	assert.NotContains(t, strings.ToLower(body), "r2 transport",
		"response leaked the upstream-err prefix; writeUpstreamError contract violated")

	// Generic envelope contract: error.code is stable, error.message is opaque.
	var env map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	errObj, ok := env["error"].(map[string]any)
	require.True(t, ok, "response missing error envelope: %s", body)
	assert.Equal(t, "r2_list_failed", errObj["code"])
	assert.Equal(t, "upstream call failed", errObj["message"])
}
