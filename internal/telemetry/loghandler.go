package telemetry

import (
	"context"
	"log/slog"

	"github.com/getsentry/sentry-go"
)

func NewLogHandler(inner slog.Handler) slog.Handler {
	return logHandler{inner: inner}
}

type logHandler struct{ inner slog.Handler }

func (h logHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h logHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := FromContext(ctx).LogAttrs()
	if span := sentry.SpanFromContext(ctx); span != nil {
		if span.TraceID != (sentry.TraceID{}) {
			attrs = append(attrs, slog.String("trace_id", span.TraceID.String()))
		}
		if span.SpanID != (sentry.SpanID{}) {
			attrs = append(attrs, slog.String("span_id", span.SpanID.String()))
		}
	}
	if len(attrs) == 0 {
		return h.inner.Handle(ctx, r)
	}
	nr := r.Clone()
	nr.AddAttrs(attrs...)
	return h.inner.Handle(ctx, nr)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{inner: h.inner.WithGroup(name)}
}
