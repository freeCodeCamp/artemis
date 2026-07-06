package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteGitHubProbeError_RateLimitedTo429(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), code: 200}
	writeGitHubProbeError(sw, auth.ErrGitHubRateLimited)
	assert.Equal(t, http.StatusTooManyRequests, sw.code)
	assert.Equal(t, "rate_limited", sw.errCode)
}

func TestWriteGitHubProbeError_OtherTo503(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), code: 200}
	writeGitHubProbeError(sw, errors.New("network boom"))
	assert.Equal(t, http.StatusServiceUnavailable, sw.code)
	assert.Equal(t, "upstream_unavailable", sw.errCode)
}

func TestDeployInit_RateLimitedProbe_Returns429(t *testing.T) {
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins:  map[string]string{"good": "alice"},
			authorizeErr: auth.ErrGitHubRateLimited,
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		newFakeR2())

	chain := RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.DeployInit)))
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", strings.NewReader(`{"site":"www","sha":"abcdef1234"}`))
	r.Header.Set("Authorization", "Bearer good")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chain.ServeHTTP(w, r)

	require.Equal(t, http.StatusTooManyRequests, w.Code, "handler re-probe rate-limit maps to 429, not blanket 503")
}
