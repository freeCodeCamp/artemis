// Package observability wires Sentry error monitoring, performance
// tracing, and the slog -> Sentry Logs bridge for artemis.
//
// The whole feature is gated on a non-empty DSN: with no DSN the SDK is
// never initialised and every helper degrades to a no-op, so dev and
// test runs are unaffected and no events leave the process.
//
// HARD invariant (artemis is the sole holder of the R2 admin key, the
// JWT signing key, and the GitHub App private key): no secret may reach
// Sentry. There are THREE egress paths and each has its own scrubber —
// they must not diverge:
//
//   - Issues       — scrubEvent via BeforeSend.
//   - Transactions — scrubEvent via BeforeSendTransaction.
//   - Logs         — scrubLog via BeforeSendLog (a DISTINCT hook; the
//     SDK does not run BeforeSend on log envelopes).
//
// Issues come only from the explicit CaptureException paths (the HTTP
// middleware, writeUpstreamError, and the helpers below), each tagged by
// op + fingerprint. The slog tee emits LOGS only, never issues.
package observability

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
	sentryslog "github.com/getsentry/sentry-go/slog"
)

const flushTimeout = 2 * time.Second

// probePaths are the high-frequency k8s probe routes. Their transactions
// are dropped from tracing; matched by exact path, never substring, so a
// business route whose name merely contains "metrics" is not suppressed.
var probePaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// secretPatterns redact secret-shaped substrings from any free text bound
// for Sentry (exception values, log bodies, string attributes). Defense
// in depth: artemis's own error wrapping does not embed secrets today,
// but the upstream driver strings it wraps are outside our control.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN[^-]*PRIVATE KEY-----.*?-----END[^-]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]+`),
	regexp.MustCompile(`(?i)\b(password|passwd|token|secret|api[_-]?key|signing[_-]?key|jwt)\b\s*[=:]\s*\S+`),
}

// sensitiveKeySubstrings names log-attribute keys whose values must never
// reach Sentry: secrets (HARD invariant) plus client IP (PII parity with
// SendDefaultPII:false). Matched as a lowercase substring.
var sensitiveKeySubstrings = []string{
	"authorization", "bearer", "token", "secret", "password", "passwd",
	"jwt", "private_key", "privatekey", "signing", "credential", "pem",
	"cookie", "remote",
}

// Config carries the Sentry settings resolved from config.SentryConfig
// plus the build-derived release identifier.
type Config struct {
	DSN              string
	Environment      string
	Release          string
	TracesSampleRate float64
	Debug            bool
}

// Init initialises the global Sentry client. It returns a flush function
// that MUST be deferred by the caller so buffered events are delivered
// before exit, plus an `enabled` flag used to decide whether to wire the
// slog bridge. When cfg.DSN is empty the SDK is left uninitialised:
// flush is a no-op, enabled is false, and every capture helper here
// becomes a no-op.
func Init(cfg Config) (flush func(), enabled bool, err error) {
	noop := func() {}
	if cfg.DSN == "" {
		return noop, false, nil
	}
	rate := cfg.TracesSampleRate
	if err = sentry.Init(sentry.ClientOptions{
		Dsn:                   cfg.DSN,
		Environment:           cfg.Environment,
		Release:               cfg.Release,
		TracesSampleRate:      rate,
		EnableLogs:            true,
		SendDefaultPII:        false, // never auto-attach request headers / client IPs
		AttachStacktrace:      true,
		Debug:                 cfg.Debug,
		BeforeSend:            scrubEvent,
		BeforeSendTransaction: scrubEvent,
		BeforeSendLog:         scrubLog,
		TracesSampler: sentry.TracesSampler(func(sc sentry.SamplingContext) float64 {
			name := ""
			if sc.Span != nil {
				name = sc.Span.Name
			}
			return probeSampleRate(name, rate)
		}),
	}); err != nil {
		return noop, false, err
	}
	return func() { sentry.Flush(flushTimeout) }, true, nil
}

// probeSampleRate drops k8s liveness / readiness / metrics probe
// transactions and samples everything else at base. The span name has
// the shape "<METHOD> <path>"; the path is matched exactly against
// probePaths.
func probeSampleRate(spanName string, base float64) float64 {
	path := spanName
	if i := strings.LastIndex(spanName, " "); i >= 0 {
		path = spanName[i+1:]
	}
	if _, ok := probePaths[path]; ok {
		return 0
	}
	return base
}

// redactSecrets replaces secret-shaped substrings with a marker. Safe on
// the empty string.
func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// isSensitiveKey reports whether a log-attribute key must be dropped.
func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range sensitiveKeySubstrings {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

// scrubEvent is the BeforeSend / BeforeSendTransaction hook: it strips
// secrets from issue and transaction events before delivery. The
// Authorization / Cookie / Proxy-Authorization headers carry GitHub
// bearer tokens and deploy-session JWTs; the body can be raw artifact
// bytes; the query string is cleared as defense in depth. Breadcrumbs
// are dropped wholesale — they ride along on every event (including the
// Request-less CaptureBackground / CaptureFatal events, which is why
// this runs before the nil-Request return) and artemis adds no
// breadcrumbs worth the secret-exposure risk. Exception values and the
// message are redacted because BeforeSend does not otherwise touch them.
func scrubEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return event
	}
	event.Message = redactSecrets(event.Message)
	for i := range event.Exception {
		event.Exception[i].Value = redactSecrets(event.Exception[i].Value)
	}
	event.Breadcrumbs = nil
	if event.Request == nil {
		return event
	}
	for k := range event.Request.Headers {
		switch strings.ToLower(k) {
		case "authorization", "cookie", "proxy-authorization", "x-forwarded-for":
			delete(event.Request.Headers, k)
		}
	}
	event.Request.Cookies = ""
	event.Request.Data = ""
	event.Request.QueryString = ""
	return event
}

// scrubLog is the BeforeSendLog hook: the logs path bypasses BeforeSend
// entirely, so without this the slog tee would be an unscrubbed egress
// channel. It redacts the body and every string attribute value, and
// drops attributes whose key is sensitive (secret or client IP).
func scrubLog(log *sentry.Log) *sentry.Log {
	if log == nil {
		return log
	}
	log.Body = redactSecrets(log.Body)
	for k, v := range log.Attributes {
		if isSensitiveKey(k) {
			delete(log.Attributes, k)
			continue
		}
		if v.Type() == attribute.STRING {
			log.Attributes[k] = attribute.StringValue(redactSecrets(v.AsString()))
		}
	}
	return log
}

// NewSlogHandler returns a slog.Handler that forwards records to Sentry
// as LOGS only. EventLevel MUST be an explicit empty slice: a nil
// EventLevel defaults to {Error,Fatal} (sentryslog v0.46.2), which would
// convert every slog.Error into a Sentry issue and double-capture
// alongside the explicit CaptureException paths. Issues come only from
// those paths. LogLevel is gated to levels at or above minLevel so
// Sentry Logs never carry more volume than the stdout handler.
func NewSlogHandler(minLevel slog.Level) slog.Handler {
	var logLevels []slog.Level
	for _, l := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if l >= minLevel {
			logLevels = append(logLevels, l)
		}
	}
	return sentryslog.Option{
		EventLevel: []slog.Level{},
		LogLevel:   logLevels,
	}.NewSentryHandler(context.Background())
}

// CaptureBackground reports an error raised outside any HTTP request
// (e.g. the registry refresh goroutine). op becomes a tag and the
// fingerprint so the failures group on their own. No-op when disabled.
func CaptureBackground(op string, err error) {
	if IsTransient(err) {
		slog.Warn("background op transient error (not reported to sentry)", "op", op, "err", err)
		return
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("op", op)
		scope.SetFingerprint([]string{op})
		sentry.CaptureException(err)
	})
}

func IsTransient(err error) bool {
	return errors.Is(err, context.Canceled) || pg.IsInRecovery(err)
}

// CaptureFatal reports a boot/fatal error at level fatal and flushes
// synchronously, since the process is about to exit. No-op when disabled.
func CaptureFatal(err error) {
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelFatal)
		scope.SetTag("op", "boot")
		sentry.CaptureException(err)
	})
	sentry.Flush(flushTimeout)
}

// NewMultiHandler tees one slog record to every non-nil handler. It is
// the fan-out that keeps stdout the source of truth for Loki while
// mirroring records to Sentry Logs.
func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	hs := make([]slog.Handler, 0, len(handlers))
	for _, h := range handlers {
		if h != nil {
			hs = append(hs, h)
		}
	}
	return multiHandler{handlers: hs}
}

type multiHandler struct{ handlers []slog.Handler }

func (m multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return multiHandler{handlers: hs}
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return multiHandler{handlers: hs}
}
