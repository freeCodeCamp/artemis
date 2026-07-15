package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

func auditHandlers(t *testing.T, fa *fakeAudit) *Handlers {
	t.Helper()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Audit = fa
	return h
}

func getAudit(h *Handlers, target string) *httptest.ResponseRecorder {
	return withChiRoute(http.MethodGet, "/api/audit", target, nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.AuditList))).ServeHTTP, context.Background())
}

func TestAuditList_FiltersAndReturns(t *testing.T) {
	fa := &fakeAudit{listResult: []pg.AuditRecord{
		{ID: 2, Actor: "alice", Action: "repo.approve", Outcome: "success", Detail: map[string]any{"name": "app"}},
		{ID: 1, Actor: "bob", Action: "site.promote", Site: "www", Outcome: "success"},
	}}
	h := auditHandlers(t, fa)

	w := getAudit(h, "/api/audit?actor=alice&action=repo.approve&site=www&limit=10&offset=5")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Equal(t, "alice", fa.lastFilter.Actor)
	assert.Equal(t, "repo.approve", fa.lastFilter.Action)
	assert.Equal(t, "www", fa.lastFilter.Site)
	assert.Equal(t, 10, fa.lastFilter.Limit)
	assert.Equal(t, 5, fa.lastFilter.Offset)

	var rows []AuditRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
	assert.Equal(t, "alice", rows[0].Actor)
	assert.Equal(t, "app", rows[0].Detail["name"])
}

func TestAuditList_InvalidSince(t *testing.T) {
	h := auditHandlers(t, &fakeAudit{})
	w := getAudit(h, "/api/audit?since=notatimestamp")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var env map[string]map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, "invalid_since", env["error"]["code"])
}

func TestAuditList_InvalidLimit(t *testing.T) {
	h := auditHandlers(t, &fakeAudit{})
	w := getAudit(h, "/api/audit?limit=-3")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var env map[string]map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, "invalid_limit", env["error"]["code"])
}

func TestAuditList_AnyBearerNoTeamGate(t *testing.T) {
	nobodyGH := &fakeGH{
		tokenLogins: map[string]string{"tok": "nobody"},
		userTeams:   map[string]map[string]bool{"nobody": {}},
	}
	h, _ := newTestHandlers(t, nobodyGH, standardSites(), newFakeR2())
	h.Audit = &fakeAudit{}

	w := getAudit(h, "/api/audit")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String(),
		"audit read is open to any authenticated GitHub bearer, no team gate")
}

func TestAuditList_NilStoreIs503(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	w := getAudit(h, "/api/audit")
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String(),
		"PG-less deploy-only mode leaves h.Audit nil; the guard must 503, never nil-deref panic into a 500")
	var env map[string]map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	assert.Equal(t, "audit_unavailable", env["error"]["code"])
}

func TestAuditList_ReadFailureIs502(t *testing.T) {
	h := auditHandlers(t, &fakeAudit{listErr: errors.New("pg down")})
	w := getAudit(h, "/api/audit")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
}
