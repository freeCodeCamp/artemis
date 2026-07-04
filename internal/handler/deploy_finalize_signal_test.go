package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployFinalize_BytesUnavailable_ReportsSignal(t *testing.T) {
	hub, ft := newHubWithTransport(t)
	store := newFakeR2()
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)

	deployID := "20260420-141522-abc1234"
	prefix := "www/deploys/" + deployID + "/"
	store.objects[prefix+"index.html"] = []byte("<h1>hi</h1>")
	store.listErr = errors.New("r2 list bytes: SlowDown 503 throttled")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})
	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize",
		body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		sentry.SetHubOnContext(context.Background(), hub),
	)
	hub.Flush(time.Second)

	require.Equal(t, http.StatusOK, w.Code, "bytes-unavailable must NOT gate finalize (DHP-1)")

	store.mu.Lock()
	alias := store.aliases["www/preview"]
	store.mu.Unlock()
	require.Equal(t, deployID, alias, "alias still written despite bytes failure")

	require.Len(t, ft.events, 1,
		"real R2 degradation on finalize must raise a grouped Sentry issue, not vanish into a WARN log")
	assert.Equal(t, "r2.list.bytes.finalize", ft.events[0].Tags["op"])
	assert.Equal(t, []string{"upstream", "r2.list.bytes.finalize"}, ft.events[0].Fingerprint)
}
