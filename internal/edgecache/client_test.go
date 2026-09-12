package edgecache_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/edgecache"
)

type capturedRequest struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

func startZone(t *testing.T, status int, reply string) (*edgecache.Client, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return &edgecache.Client{
		HTTP:    srv.Client(),
		BaseURL: srv.URL,
		ZoneID:  "zone123",
		Token:   "tok",
	}, got
}

func TestPurgeHosts_PostsTheHostsToTheZone(t *testing.T) {
	c, got := startZone(t, http.StatusOK, `{"success":true,"errors":[],"result":{"id":"zone123"}}`)

	require.NoError(t, c.PurgeHosts(context.Background(), []string{"www.freecode.camp"}))

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/zones/zone123/purge_cache", got.path)
	assert.Equal(t, "Bearer tok", got.auth)
	assert.Equal(t, map[string]any{"hosts": []any{"www.freecode.camp"}}, got.body)
}

func TestPurgeHosts_FailsOnAnAPIError(t *testing.T) {
	c, _ := startZone(t, http.StatusForbidden,
		`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)

	err := c.PurgeHosts(context.Background(), []string{"www.freecode.camp"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authentication error")
}

func TestPurgeHosts_FailsWhenSuccessIsFalse(t *testing.T) {
	c, _ := startZone(t, http.StatusOK,
		`{"success":false,"errors":[{"code":1107,"message":"hosts purge needs a plan"}]}`)

	err := c.PurgeHosts(context.Background(), []string{"www.freecode.camp"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1107")
}

func TestPurgeHosts_RefusesAnEmptyHostList(t *testing.T) {
	c, got := startZone(t, http.StatusOK, `{"success":true}`)

	require.Error(t, c.PurgeHosts(context.Background(), nil))
	assert.Empty(t, got.method, "no request must leave for an empty purge")
}

func TestPurgeHosts_QuotesANonJSONErrorBody(t *testing.T) {
	body := "<html>bad gateway\nfrom the proxy</html>" + strings.Repeat("x", 300)
	c, _ := startZone(t, http.StatusBadGateway, body)

	err := c.PurgeHosts(context.Background(), []string{"www.freecode.camp"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status=502")
	assert.Contains(t, err.Error(), `bad gateway\nfrom the proxy`, "control characters are escaped, not raw")
	assert.NotContains(t, err.Error(), strings.Repeat("x", 257), "the body is cut to 256 bytes")
}
