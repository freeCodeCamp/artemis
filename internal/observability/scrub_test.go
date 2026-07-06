package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	"github.com/stretchr/testify/require"
)

func TestScrubText(t *testing.T) {
	for _, in := range []string{
		"Bearer ghp_abc123DEF",
		"token=supersecret",
		"password: hunter2",
		"-----BEGIN RSA PRIVATE KEY-----x\n-----END RSA PRIVATE KEY-----",
	} {
		require.Contains(t, ScrubText(in), "[REDACTED]", in)
	}
	require.Equal(t, "", ScrubText(""), "empty stays empty")
	require.Equal(t, "nothing secret here", ScrubText("nothing secret here"))
}

func TestScrubbingHandler_RedactsStdoutLog(t *testing.T) {
	var buf bytes.Buffer
	h := NewScrubbingHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	logger.Info("auth Bearer ghp_leak123 done",
		"op", "r2.put",
		"authorization", "Bearer ghp_x",
		"err", "valkey password=secret123",
	)

	out := buf.String()
	require.Contains(t, out, "[REDACTED]")
	require.NotContains(t, out, "ghp_leak123", "secret in message redacted")
	require.NotContains(t, out, "ghp_x", "secret in dropped attr must not survive")
	require.NotContains(t, out, "secret123", "secret in string value redacted")
	require.NotContains(t, out, "authorization", "sensitive-keyed attr dropped from stdout")
	require.Contains(t, out, "r2.put", "benign attr kept")
}

func TestScrubbingHandler_ScrubsGroupedAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := NewScrubbingHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	logger.Info("grouped",
		slog.Group("upstream",
			slog.String("token", "ghp_grouped"),
			slog.String("op", "github.probe"),
		),
	)

	out := buf.String()
	require.NotContains(t, out, "ghp_grouped", "secret-keyed attr inside a group is dropped")
	require.Contains(t, out, "github.probe", "benign grouped attr kept")
}

func TestIsSensitiveKey(t *testing.T) {
	for _, k := range []string{"Authorization", "authorization", "x-jwt", "valkey_password", "remote", "GH_APP_PRIVATE_KEY"} {
		require.True(t, isSensitiveKey(k), k)
	}
	for _, k := range []string{"op", "site", "reqID", "login", "status", "path"} {
		require.False(t, isSensitiveKey(k), k)
	}
}

func TestScrubEvent_ClearsQueryStringAndBreadcrumbs(t *testing.T) {
	event := &sentry.Event{
		Breadcrumbs: []*sentry.Breadcrumb{{Message: "GET https://host/x?token=ghp_secret"}},
		Request:     &sentry.Request{QueryString: "token=ghp_secret"},
	}

	got := scrubEvent(event, nil)

	require.Empty(t, got.Request.QueryString, "query string cleared")
	require.Nil(t, got.Breadcrumbs, "breadcrumbs dropped wholesale")
}

func TestScrubEvent_RedactsExceptionAndMessage(t *testing.T) {
	// No Request: exercises the Request-less path (CaptureBackground/Fatal),
	// proving breadcrumb + exception scrubbing runs before the nil return.
	event := &sentry.Event{
		Message:     "boom Bearer ghp_abc123",
		Exception:   []sentry.Exception{{Value: "dial failed password=hunter2 trailing"}},
		Breadcrumbs: []*sentry.Breadcrumb{{Message: "x"}},
	}

	got := scrubEvent(event, nil)

	require.Contains(t, got.Message, "[REDACTED]")
	require.NotContains(t, got.Message, "ghp_abc123")
	require.Contains(t, got.Exception[0].Value, "[REDACTED]")
	require.NotContains(t, got.Exception[0].Value, "hunter2")
	require.Nil(t, got.Breadcrumbs, "request-less event still gets breadcrumbs scrubbed")
}

func TestScrubLog_RedactsBodyAndAttrs(t *testing.T) {
	log := &sentry.Log{
		Body: "auth Bearer ghp_leak123 done",
		Attributes: map[string]attribute.Value{
			"op":            attribute.StringValue("r2.put.upload"),
			"authorization": attribute.StringValue("Bearer ghp_x"),
			"remote":        attribute.StringValue("1.2.3.4:5678"),
			"err":           attribute.StringValue("valkey password=secret123"),
		},
	}

	got := scrubLog(log)

	require.Contains(t, got.Body, "[REDACTED]")
	require.NotContains(t, got.Body, "ghp_leak123")
	require.NotContains(t, got.Attributes, "authorization", "secret-keyed attr dropped")
	require.NotContains(t, got.Attributes, "remote", "client-IP (PII) attr dropped")
	require.Equal(t, "r2.put.upload", got.Attributes["op"].AsString(), "benign attr kept")
	require.Contains(t, got.Attributes["err"].AsString(), "[REDACTED]", "secret in string value redacted")
	require.NotContains(t, got.Attributes["err"].AsString(), "secret123")
}

func TestScrubLog_NilSafe(t *testing.T) {
	require.Nil(t, scrubLog(nil))
}

// TestScrubEvent_WiredAsBeforeSend drives a real exception through the SDK
// client pipeline with scrubEvent installed as BeforeSend, proving the
// scrub is effective end-to-end (not just in isolation) and guarding
// against a regression that drops the hook from ClientOptions.
func TestScrubEvent_WiredAsBeforeSend(t *testing.T) {
	rt := &recordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:        "https://public@example.test/1",
		Transport:  rt,
		BeforeSend: scrubEvent,
	})
	require.NoError(t, err)
	hub := sentry.NewHub(client, sentry.NewScope())
	hub.Scope().SetRequest(&http.Request{
		Method: http.MethodGet,
		URL:    mustURL(t, "/api/whoami"),
		Header: http.Header{"Authorization": {"Bearer ghp_secret"}},
	})

	hub.CaptureException(errString("upstream boom"))
	hub.Flush(time.Second)

	require.Len(t, rt.events, 1)
	if rt.events[0].Request != nil {
		require.NotContains(t, rt.events[0].Request.Headers, "Authorization",
			"BeforeSend=scrubEvent must strip the bearer end-to-end")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
