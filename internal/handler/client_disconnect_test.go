package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

func TestWriteUpstreamError_ClientCanceled_NoSentryNo502(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/site/www/promote", nil)
	rec := httptest.NewRecorder()

	err := fmt.Errorf("site lock www: %w", context.Canceled)
	writeUpstreamError(rec, req, http.StatusBadGateway, "site_lock_failed", "pg.lock.site", err)
	hub.Flush(time.Second)

	require.Empty(t, ft.events, "client cancel must not reach Sentry")
	require.NotEqual(t, http.StatusBadGateway, rec.Code)
	require.NotContains(t, rec.Body.String(), "upstream call failed")
}

func TestWriteUpstreamError_DeadlineExceeded_StillCaptures(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	ctx := sentry.SetHubOnContext(t.Context(), hub)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/site/www/deploys", nil)
	rec := httptest.NewRecorder()

	err := fmt.Errorf("r2 list: %w", context.DeadlineExceeded)
	writeUpstreamError(rec, req, http.StatusBadGateway, "r2_list_failed", "r2.list", err)
	hub.Flush(time.Second)

	require.Len(t, ft.events, 1, "a genuine timeout is a real signal and must capture")
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestSitePromote_ClientAbort_NoUpstreamError(t *testing.T) {
	h, _ := newTestHandlers(t, authedGH(), standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeErrLocker{err: fmt.Errorf("site lock %s: %w", "www", context.Canceled)}

	body, _ := json.Marshal(SitePromoteRequest{DeployID: "20260420-141522-abc1234"})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.NotEqual(t, http.StatusBadGateway, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "upstream call failed")
}

func TestWriteUpstreamError_AbortedClient_LogsStatus499(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, code: 200}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/site/www/promote", nil)

	err := fmt.Errorf("site lock www: %w", context.Canceled)
	writeUpstreamError(sw, req, http.StatusBadGateway, "site_lock_failed", "pg.lock.site", err)

	require.Equal(t, statusClientClosedRequest, sw.code,
		"the access log must record 499 on a real disconnect, not a default 200")
	require.Equal(t, "client_closed_request", sw.errCode)
}
