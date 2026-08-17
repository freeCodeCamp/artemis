package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingBeginner struct {
	mu    sync.Mutex
	calls [][2]string
	err   error
}

func (b *recordingBeginner) BeginDeploy(_ context.Context, site, id string, _ time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, [2]string{site, id})
	return b.err
}

func newPendingHandlers(t *testing.T) *Handlers {
	t.Helper()
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"team-eng": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())
	h.DeployPrefix = mustDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	return h
}

func initRequest(t *testing.T, h *Handlers) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(DeployInitRequest{Site: "www", SHA: "abc1234567"})
	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	h.DeployInit(w, r)
	return w
}

func TestDeployInit_RecordsThePendingDeployInTheStorageKeyspace(t *testing.T) {
	h := newPendingHandlers(t)
	beginner := &recordingBeginner{}
	h.Pending = beginner

	rec := initRequest(t, h)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, beginner.calls, 1,
		"an init that records nothing leaves any bytes the client then uploads unowned by every reaper, "+
			"which is the whole orphan class reconcile exists to scan for")
	require.Equal(t, "www.freecode.camp", h.DeployPrefix.SiteDirname("www"),
		"under the default format slug and dirname are the same string, which makes the assertion below "+
			"vacuous; this fixture must run the production FQDN shape")
	assert.Equal(t, "www.freecode.camp", beginner.calls[0][0],
		"finalize upserts ON CONFLICT (site, id) using SiteDirname, so a pending row written under the slug "+
			"would never be promoted and would be reaped while the deploy is live")
}

func TestDeployInit_SucceedsWhenThePendingWriteFails(t *testing.T) {
	h := newPendingHandlers(t)
	h.Pending = &recordingBeginner{err: errors.New("pg down")}

	rec := initRequest(t, h)

	assert.Equal(t, http.StatusOK, rec.Code,
		"the index is optional wiring (deploy-only mode runs without one), so a bookkeeping write must never "+
			"turn a database blip into a total deploy outage")
}

func TestDeployInit_SucceedsWithNoPendingWriterWired(t *testing.T) {
	h := newPendingHandlers(t)
	h.Pending = nil

	rec := initRequest(t, h)

	assert.Equal(t, http.StatusOK, rec.Code)
}
