package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteUpstreamError_LogsActorSiteDeploy(t *testing.T) {
	cap := captureAccessLog(t)

	sc := telemetry.New("req-x")
	sc.SetActor("alice")
	sc.SetResource("www", "d-9")
	ctx := telemetry.NewContext(context.Background(), sc)

	r := httptest.NewRequest(http.MethodPost, "/api/site/www/promote", nil).WithContext(ctx)
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), code: 200}

	writeUpstreamError(sw, r, http.StatusBadGateway, "r2_put_failed", "r2.put.test", errors.New("boom"))

	m, ok := cap.findAction("upstream.error", "")
	require.True(t, ok, "upstream error log line captured")
	assert.Equal(t, "alice", m["actor"])
	assert.Equal(t, "www", m["site"])
	assert.Equal(t, "d-9", m["deploy_id"])
	assert.Equal(t, "req-x", m["request_id"])
}
