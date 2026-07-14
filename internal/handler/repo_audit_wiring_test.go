package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

func adminStaffRepoGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"staff": true, "apollo-11-approvers": true}},
	}
}

func repoAuditHandlers(t *testing.T, store RepoStore, creator RepoCreator) (*Handlers, *fakeAudit) {
	t.Helper()
	h := repoHandlers(t, adminStaffRepoGH(), store, creator)
	fa := &fakeAudit{}
	h.Audit = fa
	return h, fa
}

func TestRepoCreate_RecordsExactlyOneAudit(t *testing.T) {
	h, fa := repoAuditHandlers(t, newFakeRepoStore(), &fakeRepoCreator{})
	body, _ := json.Marshal(RepoCreateRequest{Name: "learn-python-rpg"})
	w := withChiRoute(http.MethodPost, "/api/repo", "/api/repo", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoCreate))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a repo create records exactly one audit row")
	assert.Equal(t, "repo.create", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "success", fa.events[0].Outcome)
	assert.Equal(t, "learn-python-rpg", fa.events[0].Detail["name"])
	assert.NotEmpty(t, fa.events[0].Detail["id"])
}

func TestRepoApprove_RecordsExactlyOneAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "live", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	creator := &fakeRepoCreator{created: githubapp.Created{URL: "https://github.com/freeCodeCamp-Universe/live"}}
	h, fa := repoAuditHandlers(t, store, creator)

	w := withChiRoute(http.MethodPost, "/api/repo/{id}/approve", "/api/repo/"+created.ID+"/approve", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoApprove))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a repo approve records exactly one audit row")
	assert.Equal(t, "repo.approve", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "success", fa.events[0].Outcome)
	assert.Equal(t, "live", fa.events[0].Detail["name"])
}

func TestRepoApprove_FailureRecordsAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "boom", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	creator := &fakeRepoCreator{createErr: &githubapp.UserFacingError{Msg: "template \"x\" is not accessible"}}
	h, fa := repoAuditHandlers(t, store, creator)

	w := withChiRoute(http.MethodPost, "/api/repo/{id}/approve", "/api/repo/"+created.ID+"/approve", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoApprove))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a failed approve still records exactly one audit row")
	assert.Equal(t, "repo.approve", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "approved_failed", fa.events[0].Outcome)
}

func TestRepoReject_RecordsExactlyOneAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "r", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	h, fa := repoAuditHandlers(t, store, &fakeRepoCreator{})

	body, _ := json.Marshal(RepoRejectRequest{Reason: "out of scope"})
	w := withChiRoute(http.MethodPost, "/api/repo/{id}/reject", "/api/repo/"+created.ID+"/reject", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoReject))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a repo reject records exactly one audit row")
	assert.Equal(t, "repo.reject", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "success", fa.events[0].Outcome)
	assert.Equal(t, "out of scope", fa.events[0].Detail["reason"])
}

func TestRepoDelete_RecordsExactlyOneAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "x", Owner: "freeCodeCamp-Universe", Visibility: reporequest.VisibilityPrivate, RequestedBy: "alice"})
	h, fa := repoAuditHandlers(t, store, &fakeRepoCreator{})

	w := withChiRoute(http.MethodDelete, "/api/repo/{id}", "/api/repo/"+created.ID, nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoDelete))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	require.Len(t, fa.events, 1, "a repo delete records exactly one audit row")
	assert.Equal(t, "repo.delete", fa.events[0].Action)
	assert.Equal(t, "alice", fa.events[0].Actor)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestRepoAudit_CarriesRequestID(t *testing.T) {
	h, fa := repoAuditHandlers(t, newFakeRepoStore(), &fakeRepoCreator{})
	body, _ := json.Marshal(RepoCreateRequest{Name: "traced-repo"})
	w := withChiRoute(http.MethodPost, "/api/repo", "/api/repo", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoCreate))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	require.Len(t, fa.events, 1)
	assert.NotEmpty(t, fa.events[0].RequestID, "audit row correlates to the request via request_id")
}

func TestRepoReject_AuditCapturesRepoName(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "scope-creep", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	h, fa := repoAuditHandlers(t, store, &fakeRepoCreator{})

	body, _ := json.Marshal(RepoRejectRequest{Reason: "duplicate"})
	w := withChiRoute(http.MethodPost, "/api/repo/{id}/reject", "/api/repo/"+created.ID+"/reject", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoReject))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1)
	assert.Equal(t, "scope-creep", fa.events[0].Detail["name"], "reject audit must name the repo, not just the queue id")
}

func TestRepoReject_RejectsOverlongReasonBeforeAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "r", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	h, fa := repoAuditHandlers(t, store, &fakeRepoCreator{})

	body, _ := json.Marshal(RepoRejectRequest{Reason: strings.Repeat("x", maxRepoDescriptionLen+1)})
	w := withChiRoute(http.MethodPost, "/api/repo/{id}/reject", "/api/repo/"+created.ID+"/reject", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoReject))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Empty(t, fa.events, "an over-long reason is refused before any durable audit row is written")
}

func TestRepoApprove_TransientRetryRecordsNoAudit(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "transient", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	_, err := store.Approve(context.Background(), created.ID, "alice")
	require.NoError(t, err)
	creator := &fakeRepoCreator{createErr: &githubapp.UserFacingError{Msg: "temporarily unavailable", Retryable: true}}
	h, fa := repoAuditHandlers(t, store, creator)

	w := withChiRoute(http.MethodPost, "/api/repo/{id}/approve", "/api/repo/"+created.ID+"/approve", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoApprove))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Empty(t, fa.events, "a transient retry writes no audit row (no terminal outcome yet)")
}

func TestRepoDelete_AuditCapturesRepoName(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "abandoned-app", Owner: "freeCodeCamp-Universe", Visibility: reporequest.VisibilityPrivate, RequestedBy: "alice"})
	h, fa := repoAuditHandlers(t, store, &fakeRepoCreator{})

	w := withChiRoute(http.MethodDelete, "/api/repo/{id}", "/api/repo/"+created.ID, nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.RepoDelete))).ServeHTTP, context.Background())
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	require.Len(t, fa.events, 1)
	assert.Equal(t, "abandoned-app", fa.events[0].Detail["name"], "delete audit must name the repo (fetched before delete)")
}
