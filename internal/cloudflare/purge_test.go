package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "test-placeholder-value"

func newStub(t *testing.T, status int, body string, capture func(*http.Request, []byte)) *PurgeClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if capture != nil {
			capture(r, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &PurgeClient{ZoneID: "zone123", Token: testToken, BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestPurgeHosts_PostsHostsToTheZoneEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody purgeRequest
	c := newStub(t, http.StatusOK, `{"success":true}`, func(r *http.Request, raw []byte) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.Unmarshal(raw, &gotBody)
	})

	require.NoError(t, c.PurgeHosts(context.Background(), []string{"a.freecode.camp", "b.freecode.camp"}))

	assert.Equal(t, "/zones/zone123/purge_cache", gotPath)
	assert.Equal(t, "Bearer "+testToken, gotAuth)
	assert.Equal(t, []string{"a.freecode.camp", "b.freecode.camp"}, gotBody.Hosts)
}

func TestPurgeHosts_FailsWhenTheAPIReportsFailureOnA200(t *testing.T) {
	c := newStub(t, http.StatusOK,
		`{"success":false,"errors":[{"code":1012,"message":"Request must contain one of"}]}`, nil)

	err := c.PurgeHosts(context.Background(), []string{"a.freecode.camp"})

	require.Error(t, err, "Cloudflare answers 200 with success=false, so the status code alone is not the verdict")
	assert.Contains(t, err.Error(), "1012")
}

func TestPurgeHosts_FailsOnNon200(t *testing.T) {
	c := newStub(t, http.StatusForbidden,
		`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`, nil)

	err := c.PurgeHosts(context.Background(), []string{"a.freecode.camp"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "http 403")
}

func TestPurgeHosts_NeverPutsTheCredentialInAnError(t *testing.T) {
	c := newStub(t, http.StatusForbidden, `{"success":false}`, nil)

	err := c.PurgeHosts(context.Background(), []string{"a.freecode.camp"})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), testToken,
		"the error reaches logs and Sentry, so the API credential must never travel with it")
}

func TestPurgeHosts_NoHostsIsANoOp(t *testing.T) {
	called := false
	c := newStub(t, http.StatusOK, `{"success":true}`, func(*http.Request, []byte) { called = true })

	require.NoError(t, c.PurgeHosts(context.Background(), nil))
	assert.False(t, called, "an empty host list must not spend a request or an API rate-limit slot")
}

func TestPurgeHosts_RefusesWithoutCredentials(t *testing.T) {
	c := &PurgeClient{}

	err := c.PurgeHosts(context.Background(), []string{"a.freecode.camp"})

	require.Error(t, err, "a half-configured purger must fail loudly, not silently skip the takedown")
}

func TestPurgeClient_RedactsTheCredentialWhenFormatted(t *testing.T) {
	c := PurgeClient{ZoneID: "zone123", Token: testToken}

	rendered := fmt.Sprintf("%v", c.LogValue())

	assert.NotContains(t, rendered, testToken,
		"a future %+v of the client must not spill the credential into a log line")
	assert.Contains(t, rendered, "zone123")
}
