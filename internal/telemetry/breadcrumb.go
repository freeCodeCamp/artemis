package telemetry

import (
	"context"

	"github.com/getsentry/sentry-go"
)

func Breadcrumb(ctx context.Context, category, message string) {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:     "info",
		Category: category,
		Message:  message,
		Level:    sentry.LevelInfo,
	}, nil)
}
