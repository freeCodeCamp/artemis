package telemetry

import (
	"net/http"

	sentryhttpclient "github.com/getsentry/sentry-go/httpclient"
)

const RequestIDHeader = "X-Request-Id"

type RoundTripper struct {
	base http.RoundTripper
}

func NewRoundTripper(base http.RoundTripper) *RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &RoundTripper{base: sentryhttpclient.NewSentryRoundTripper(base)}
}

func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if id := FromContext(req.Context()).ReqID; id != "" {
		req = req.Clone(req.Context())
		req.Header.Set(RequestIDHeader, id)
	}
	return rt.base.RoundTrip(req)
}
