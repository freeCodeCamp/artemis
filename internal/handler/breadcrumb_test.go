package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSitePromote_AddsBreadcrumbs(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t,
		&fakeGH{
			tokenLogins: map[string]string{"good": "alice"},
			userTeams:   map[string]map[string]bool{"alice": {"team-a": true}},
		},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		store)

	ctx := sentry.SetHubOnContext(context.Background(), hub)
	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		map[string]string{"Authorization": "Bearer good"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		ctx)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	hub.CaptureMessage("probe")
	hub.Flush(time.Second)

	require.GreaterOrEqual(t, len(ft.events), 1)
	msgs := map[string]bool{}
	for _, bc := range ft.events[len(ft.events)-1].Breadcrumbs {
		msgs[bc.Message] = true
	}
	assert.True(t, msgs["site authz resolved"], "authz-resolved breadcrumb present")
	assert.True(t, msgs["site lock acquired"], "lock-acquired breadcrumb present")
}
