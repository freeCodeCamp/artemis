package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoundTripper_SetsRequestIDHeader(t *testing.T) {
	t.Parallel()

	var got string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Get("X-Request-Id")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	rt := telemetry.NewRoundTripper(base)

	sc := telemetry.New("req-42")
	req := httptest.NewRequest(http.MethodGet, "https://api.example/x", nil).
		WithContext(telemetry.NewContext(context.Background(), sc))

	_, err := rt.RoundTrip(req)
	require.NoError(t, err)
	assert.Equal(t, "req-42", got)
	assert.Empty(t, req.Header.Get("X-Request-Id"), "caller request must not be mutated")
}

func TestRoundTripper_NoScopeNoHeader(t *testing.T) {
	t.Parallel()

	var got string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Get("X-Request-Id")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	rt := telemetry.NewRoundTripper(base)

	req := httptest.NewRequest(http.MethodGet, "https://api.example/x", nil)
	_, err := rt.RoundTrip(req)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestNewRoundTripper_NilBaseUsesDefaultTransport(t *testing.T) {
	t.Parallel()

	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get(telemetry.RequestIDHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	rt := telemetry.NewRoundTripper(nil)
	require.NotNil(t, rt)

	sc := telemetry.New("req-nil-base")
	req, err := http.NewRequestWithContext(telemetry.NewContext(context.Background(), sc), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "req-nil-base", gotID, "nil base falls back to a working transport and still injects the request id")
}
