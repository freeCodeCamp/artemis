package telemetry_test

import (
	"context"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBreadcrumb_AttachedToCapturedEvent(t *testing.T) {
	hub, tr := newTracingHub(t)
	ctx := sentry.SetHubOnContext(context.Background(), hub)

	telemetry.Breadcrumb(ctx, "repo", "creating repo")
	hub.CaptureMessage("boom")

	require.Len(t, tr.events, 1)
	var found bool
	for _, bc := range tr.events[0].Breadcrumbs {
		if bc.Category == "repo" && bc.Message == "creating repo" && bc.Level == sentry.LevelInfo {
			found = true
		}
	}
	assert.True(t, found, "Breadcrumb adds an info crumb to the hub scope; got=%+v", tr.events[0].Breadcrumbs)
}

func TestBreadcrumb_NilHubNoOp(t *testing.T) {
	telemetry.Breadcrumb(context.Background(), "cat", "msg")
}
