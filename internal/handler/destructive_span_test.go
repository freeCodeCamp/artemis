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

type tracingTransport struct{ events []*sentry.Event }

func (s *tracingTransport) Configure(sentry.ClientOptions)        {}
func (s *tracingTransport) SendEvent(e *sentry.Event)             { s.events = append(s.events, e) }
func (s *tracingTransport) Flush(time.Duration) bool              { return true }
func (s *tracingTransport) FlushWithContext(context.Context) bool { return true }
func (s *tracingTransport) Close()                                {}

func TestDestructiveFlow_BreadcrumbsAndSpans(t *testing.T) {
	tr := &tracingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:              "https://public@example.test/1",
		Transport:        tr,
		EnableTracing:    true,
		TracesSampleRate: 1.0,
	})
	require.NoError(t, err)
	hub := sentry.NewHub(client, sentry.NewScope())
	ctx := sentry.SetHubOnContext(context.Background(), hub)
	tx := sentry.StartTransaction(ctx, "promote-tx")

	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t,
		&fakeGH{tokenLogins: map[string]string{"good": "alice"}, userTeams: map[string]map[string]bool{"alice": {"team-a": true}}},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}},
		store)

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		map[string]string{"Authorization": "Bearer good"},
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		tx.Context())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	tx.Finish()
	hub.CaptureMessage("probe")
	hub.Flush(time.Second)

	var txEvent, msgEvent *sentry.Event
	for _, e := range tr.events {
		if e.Type == "transaction" {
			txEvent = e
		} else {
			msgEvent = e
		}
	}
	require.NotNil(t, txEvent, "transaction event emitted")
	require.NotNil(t, msgEvent, "probe message event emitted")

	ops := map[string]bool{}
	for _, sp := range txEvent.Spans {
		ops[sp.Op] = true
	}
	assert.True(t, ops["r2.put.alias.promote"], "promote R2 write emits a child span")

	msgs := map[string]bool{}
	for _, bc := range msgEvent.Breadcrumbs {
		msgs[bc.Message] = true
	}
	assert.True(t, msgs["production alias write"], "promote emits a destructive-write breadcrumb")
}
