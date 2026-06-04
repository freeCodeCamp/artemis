package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeOutbox struct {
	sites []string
}

func (f *fakeOutbox) EnqueueSiteChanged(_ context.Context, site string) error {
	f.sites = append(f.sites, site)
	return nil
}

type ctxCapturingOutbox struct {
	capturedDone bool
	called       bool
}

func (f *ctxCapturingOutbox) EnqueueSiteChanged(ctx context.Context, _ string) error {
	f.called = true
	select {
	case <-ctx.Done():
		f.capturedDone = true
	default:
	}
	return nil
}

func TestEmitSiteChanged_DetachedFromRequestCancellation(t *testing.T) {
	h, _ := newTestHandlers(t, &fakeGH{}, standardSites(), newFakeR2())
	ob := &ctxCapturingOutbox{}
	h.Outbox = ob

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h.emitSiteChanged(ctx, "www")

	require.True(t, ob.called, "emitSiteChanged must enqueue even when the request context is canceled")
	assert.False(t, ob.capturedDone, "enqueue context must be detached from the canceled request context")
}

func TestFinalize_EmitsSiteChanged(t *testing.T) {
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	ob := &fakeOutbox{}
	h.Outbox = ob

	deployID := "20260420-141522-abc1234"
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})
	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"www"}, ob.sites, "finalize emits site.changed for event-driven GC")
}

func TestPromote_EmitsSiteChanged(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	ob := &fakeOutbox{}
	h.Outbox = ob

	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"www"}, ob.sites)
}
