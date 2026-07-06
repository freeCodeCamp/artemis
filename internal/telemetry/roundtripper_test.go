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
