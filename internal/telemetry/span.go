package telemetry

import (
	"context"

	"github.com/getsentry/sentry-go"
)

func WithSpan(ctx context.Context, op string, fn func(context.Context) error) error {
	span := sentry.StartSpan(ctx, op)
	defer span.Finish()
	err := fn(span.Context())
	if err != nil {
		span.Status = sentry.SpanStatusInternalError
	}
	return err
}
