package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"
)

func TestRequireDeployJWT_SetsSentryUser(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	h, jwt := newTestHandlers(t, &fakeGH{},
		&fakeSites{bySite: map[string][]string{"www": {"team-a"}}}, newFakeR2())

	tok, _, err := jwt.Sign("alice", "www", "d-1")
	require.NoError(t, err)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hb := sentry.GetHubFromContext(r.Context()); hb != nil {
			hb.CaptureMessage("probe")
		}
		w.WriteHeader(http.StatusOK)
	})

	ctx := sentry.SetHubOnContext(context.Background(), hub)
	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/deploy/d-1/upload", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	RequestID(h.RequireDeployJWT(next)).ServeHTTP(rec, req)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusOK, rec.Code)
	require.GreaterOrEqual(t, len(ft.events), 1, "upload leg captured a Sentry event")
	require.Equal(t, "alice", ft.events[0].User.Username, "deploy-JWT actor mirrored onto the Sentry user")
}
