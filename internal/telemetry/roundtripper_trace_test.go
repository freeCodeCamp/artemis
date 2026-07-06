package telemetry_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutbound_TracePropagation(t *testing.T) {
	var seen http.Header
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	rt := telemetry.NewRoundTripper(base)

	span := sentry.StartSpan(context.Background(), "test.op")
	sc := telemetry.New("req-1")
	ctx := telemetry.NewContext(span.Context(), sc)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.NotEmpty(t, seen.Get(sentry.SentryTraceHeader), "outbound carries sentry-trace")
	_, hasBaggage := seen[http.CanonicalHeaderKey(sentry.SentryBaggageHeader)]
	assert.True(t, hasBaggage, "outbound carries baggage header (populated once Sentry is initialised)")
	assert.Equal(t, "req-1", seen.Get(telemetry.RequestIDHeader), "outbound still carries X-Request-Id")
}
