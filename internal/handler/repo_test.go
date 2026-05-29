package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/reporequest"
)

// fakeRepoStore is an in-memory RepoStore with the same status semantics
// as the valkey store: name-claim dedupe + CAS-on-pending resolution.
type fakeRepoStore struct {
	mu        sync.Mutex
	byID      map[string]reporequest.Request
	names     map[string]bool
	seq       int
	createErr error
	listErr   error
}

func newFakeRepoStore() *fakeRepoStore {
	return &fakeRepoStore{byID: map[string]reporequest.Request{}, names: map[string]bool{}}
}

func (f *fakeRepoStore) clock() time.Time {
	return time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC).Add(time.Duration(f.seq) * time.Second)
}

func (f *fakeRepoStore) Create(_ context.Context, req reporequest.Request) (reporequest.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return reporequest.Request{}, f.createErr
	}
	if f.names[req.Name] {
		return reporequest.Request{}, reporequest.ErrAlreadyExists
	}
	f.seq++
	req.ID = fmt.Sprintf("req_%03d", f.seq)
	req.Status = reporequest.StatusPending
	req.CreatedAt = f.clock()
	req.UpdatedAt = req.CreatedAt
	f.byID[req.ID] = req
	f.names[req.Name] = true
	return req, nil
}

func (f *fakeRepoStore) Get(_ context.Context, id string) (reporequest.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok {
		return reporequest.Request{}, reporequest.ErrNotFound
	}
	return r, nil
}

func (f *fakeRepoStore) List(_ context.Context) ([]reporequest.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]reporequest.Request, 0, len(f.byID))
	for _, r := range f.byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeRepoStore) transition(id string, fn func(reporequest.Request) (reporequest.Request, bool, error)) (reporequest.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byID[id]
	if !ok {
		return reporequest.Request{}, reporequest.ErrNotFound
	}
	next, release, err := fn(r)
	if err != nil {
		return reporequest.Request{}, err
	}
	next.UpdatedAt = f.clock()
	f.byID[id] = next
	if release {
		delete(f.names, next.Name)
	}
	return next, nil
}

func (f *fakeRepoStore) Approve(_ context.Context, id, approver string) (reporequest.Request, error) {
	return f.transition(id, func(r reporequest.Request) (reporequest.Request, bool, error) {
		if !r.Status.CanResolve() {
			return reporequest.Request{}, false, reporequest.ErrNotPending
		}
		r.Status = reporequest.StatusApproved
		r.Approver = approver
		return r, false, nil
	})
}

func (f *fakeRepoStore) Reject(_ context.Context, id, approver, reason string) (reporequest.Request, error) {
	return f.transition(id, func(r reporequest.Request) (reporequest.Request, bool, error) {
		if !r.Status.CanResolve() {
			return reporequest.Request{}, false, reporequest.ErrNotPending
		}
		r.Status = reporequest.StatusRejected
		r.Approver = approver
		r.RejectReason = reason
		return r, true, nil
	})
}

func (f *fakeRepoStore) MarkActive(_ context.Context, id, url string) (reporequest.Request, error) {
	return f.transition(id, func(r reporequest.Request) (reporequest.Request, bool, error) {
		r.Status = reporequest.StatusActive
		r.URL = url
		return r, false, nil
	})
}

func (f *fakeRepoStore) MarkFailed(_ context.Context, id, msg string) (reporequest.Request, error) {
	return f.transition(id, func(r reporequest.Request) (reporequest.Request, bool, error) {
		r.Status = reporequest.StatusFailed
		r.Error = msg
		return r, true, nil
	})
}

// fakeRepoCreator is a stub RepoCreator.
type fakeRepoCreator struct {
	created      githubapp.Created
	createErr    error
	templates    []string
	templatesErr error
	lastSpec     githubapp.CreateSpec
	createCalls  int
}

func (f *fakeRepoCreator) CreateRepo(_ context.Context, spec githubapp.CreateSpec) (githubapp.Created, error) {
	f.lastSpec = spec
	f.createCalls++
	if f.createErr != nil {
		return githubapp.Created{}, f.createErr
	}
	return f.created, nil
}

func (f *fakeRepoCreator) ListTemplates(_ context.Context) ([]string, error) {
	return f.templates, f.templatesErr
}

// repoHandlers wires a Handlers with the repo deps. repoGH is the
// Universe-org membership prober used by repo authz.
func repoHandlers(t *testing.T, repoGH *fakeGH, store RepoStore, creator RepoCreator) *Handlers {
	t.Helper()
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())
	h.RepoGH = repoGH
	h.Repos = store
	h.GitHubApp = creator
	h.RepoOrg = "freeCodeCamp-Universe"
	h.RepoCreateAuthzTeam = "staff"
	h.RepoApproveAuthzTeam = "apollo-11-approvers"
	return h
}

// staffRepoGH: alice/tok is on the Universe "staff" team.
func staffRepoGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"staff": true}},
	}
}

// adminRepoGH: boss/atok is on the Universe "apollo-11-approvers" team.
func adminRepoGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"atok": "boss"},
		userTeams:   map[string]map[string]bool{"boss": {"apollo-11-approvers": true}},
	}
}

func doReq(h *Handlers, method, target string, body []byte, login, token string, route func(*Handlers, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, target, rdr).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	route(h, w, r)
	return w
}

// withID injects a chi URL param {id} into the request route context.
func withID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestRepoCreate_HappyPath(t *testing.T) {
	h := repoHandlers(t, staffRepoGH(), newFakeRepoStore(), &fakeRepoCreator{})
	body, _ := json.Marshal(RepoCreateRequest{Name: "learn-python-rpg", Description: "a game"})
	w := doReq(h, http.MethodPost, "/api/repo", body, "alice", "tok", (*Handlers).RepoCreate)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var got RepoRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "learn-python-rpg", got.Name)
	assert.Equal(t, "freeCodeCamp-Universe", got.Owner)
	assert.Equal(t, "private", got.Visibility) // defaulted
	assert.Equal(t, "pending", got.Status)
	assert.Equal(t, "alice", got.RequestedBy)
}

func TestRepoCreate_RejectsNonStaff(t *testing.T) {
	// caller dave is on no team → RepoGH denies.
	gh := &fakeGH{tokenLogins: map[string]string{"tok": "dave"}, userTeams: map[string]map[string]bool{"dave": {}}}
	h := repoHandlers(t, gh, newFakeRepoStore(), &fakeRepoCreator{})
	body, _ := json.Marshal(RepoCreateRequest{Name: "x"})
	w := doReq(h, http.MethodPost, "/api/repo", body, "dave", "tok", (*Handlers).RepoCreate)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestRepoCreate_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body RepoCreateRequest
		code string
	}{
		{"bad name", RepoCreateRequest{Name: "-bad"}, "invalid_name"},
		{"bad visibility", RepoCreateRequest{Name: "ok", Visibility: "secret"}, "invalid_visibility"},
		{"bad template", RepoCreateRequest{Name: "ok", Template: "../escape"}, "invalid_template"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := repoHandlers(t, staffRepoGH(), newFakeRepoStore(), &fakeRepoCreator{})
			body, _ := json.Marshal(tc.body)
			w := doReq(h, http.MethodPost, "/api/repo", body, "alice", "tok", (*Handlers).RepoCreate)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			var env map[string]map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
			assert.Equal(t, tc.code, env["error"]["code"])
		})
	}
}

func TestRepoCreate_Duplicate(t *testing.T) {
	store := newFakeRepoStore()
	h := repoHandlers(t, staffRepoGH(), store, &fakeRepoCreator{})
	body, _ := json.Marshal(RepoCreateRequest{Name: "dup"})
	_ = doReq(h, http.MethodPost, "/api/repo", body, "alice", "tok", (*Handlers).RepoCreate)
	w := doReq(h, http.MethodPost, "/api/repo", body, "alice", "tok", (*Handlers).RepoCreate)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

func TestReposList_FiltersByStatusAndMine(t *testing.T) {
	store := newFakeRepoStore()
	ctx := context.Background()
	a, _ := store.Create(ctx, reporequest.Request{Name: "a", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	_, _ = store.Create(ctx, reporequest.Request{Name: "b", RequestedBy: "bob", Visibility: reporequest.VisibilityPrivate})
	// resolve a → rejected, so default pending filter excludes it.
	_, _ = store.Reject(ctx, a.ID, "boss", "")

	h := repoHandlers(t, staffRepoGH(), store, &fakeRepoCreator{})

	// default status=pending → only b
	w := doReq(h, http.MethodGet, "/api/repos", nil, "alice", "tok", (*Handlers).ReposList)
	require.Equal(t, http.StatusOK, w.Code)
	var pending []RepoRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pending))
	require.Len(t, pending, 1)
	assert.Equal(t, "b", pending[0].Name)

	// status=all&mine=1 as alice → only a (hers), regardless of status
	w = doReq(h, http.MethodGet, "/api/repos?status=all&mine=1", nil, "alice", "tok", (*Handlers).ReposList)
	require.Equal(t, http.StatusOK, w.Code)
	var mine []RepoRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &mine))
	require.Len(t, mine, 1)
	assert.Equal(t, "a", mine[0].Name)
}

func TestReposList_InvalidStatus(t *testing.T) {
	h := repoHandlers(t, staffRepoGH(), newFakeRepoStore(), &fakeRepoCreator{})
	w := doReq(h, http.MethodGet, "/api/repos?status=bogus", nil, "alice", "tok", (*Handlers).ReposList)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRepoGet(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "g", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	h := repoHandlers(t, staffRepoGH(), store, &fakeRepoCreator{})

	r := withID(httptest.NewRequest(http.MethodGet, "/api/repo/"+created.ID, nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok")), created.ID)
	w := httptest.NewRecorder()
	h.RepoGet(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	rMissing := withID(httptest.NewRequest(http.MethodGet, "/api/repo/nope", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok")), "nope")
	wMissing := httptest.NewRecorder()
	h.RepoGet(wMissing, rMissing)
	assert.Equal(t, http.StatusNotFound, wMissing.Code)
}

func approveReq(h *Handlers, id, login, token string) *httptest.ResponseRecorder {
	r := withID(httptest.NewRequest(http.MethodPost, "/api/repo/"+id+"/approve", nil).
		WithContext(contextWithLogin(context.Background(), login, token)), id)
	w := httptest.NewRecorder()
	h.RepoApprove(w, r)
	return w
}

func TestRepoApprove_OK(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "live", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	creator := &fakeRepoCreator{created: githubapp.Created{FullName: "freeCodeCamp-Universe/live", URL: "https://github.com/freeCodeCamp-Universe/live", Visibility: "private"}}
	h := repoHandlers(t, adminRepoGH(), store, creator)

	w := approveReq(h, created.ID, "boss", "atok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp RepoApproveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Outcome)
	assert.Equal(t, "active", resp.Request.Status)
	assert.Equal(t, "https://github.com/freeCodeCamp-Universe/live", resp.Request.URL)
	assert.Equal(t, "boss", resp.Request.Approver)
	// spec mapped correctly (private → Private true).
	assert.True(t, creator.lastSpec.Private)
}

func TestRepoApprove_GitHubFailureYieldsApprovedFailed(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "boom", RequestedBy: "alice", Visibility: reporequest.VisibilityPrivate})
	creator := &fakeRepoCreator{createErr: fmt.Errorf("template \"x\" is not accessible (Contents:read)")}
	h := repoHandlers(t, adminRepoGH(), store, creator)

	w := approveReq(h, created.ID, "boss", "atok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp RepoApproveResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "approved_failed", resp.Outcome)
	assert.Equal(t, "failed", resp.Request.Status)
	assert.Contains(t, resp.Request.Error, "Contents:read")
}

func TestRepoApprove_AlreadyResolved(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "race", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	creator := &fakeRepoCreator{created: githubapp.Created{URL: "u"}}
	h := repoHandlers(t, adminRepoGH(), store, creator)

	first := approveReq(h, created.ID, "boss", "atok")
	require.Equal(t, http.StatusOK, first.Code)
	second := approveReq(h, created.ID, "boss", "atok")
	assert.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assert.Equal(t, 1, creator.createCalls, "GitHub create must run exactly once")
}

func TestRepoApprove_RejectsNonAdmin(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "x", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	// staff caller is NOT on apollo-11-approvers → 403, proving authz routes via RepoGH (§V4).
	h := repoHandlers(t, staffRepoGH(), store, &fakeRepoCreator{})
	w := approveReq(h, created.ID, "alice", "tok")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestRepoApprove_NotFound(t *testing.T) {
	h := repoHandlers(t, adminRepoGH(), newFakeRepoStore(), &fakeRepoCreator{})
	w := approveReq(h, "nope", "boss", "atok")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRepoReject(t *testing.T) {
	store := newFakeRepoStore()
	created, _ := store.Create(context.Background(), reporequest.Request{Name: "r", RequestedBy: "alice", Visibility: reporequest.VisibilityPublic})
	h := repoHandlers(t, adminRepoGH(), store, &fakeRepoCreator{})

	body, _ := json.Marshal(RepoRejectRequest{Reason: "out of scope"})
	r := withID(httptest.NewRequest(http.MethodPost, "/api/repo/"+created.ID+"/reject", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), "boss", "atok")), created.ID)
	w := httptest.NewRecorder()
	h.RepoReject(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got RepoRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "rejected", got.Status)
	assert.Equal(t, "out of scope", got.RejectReason)
}

func TestRepoTemplates_SuccessAndFailSoft(t *testing.T) {
	h := repoHandlers(t, staffRepoGH(), newFakeRepoStore(), &fakeRepoCreator{templates: []string{"alpha", "beta"}})
	w := doReq(h, http.MethodGet, "/api/repo/templates", nil, "alice", "tok", (*Handlers).RepoTemplates)
	require.Equal(t, http.StatusOK, w.Code)
	var resp RepoTemplatesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, []string{"alpha", "beta"}, resp.Templates)

	hErr := repoHandlers(t, staffRepoGH(), newFakeRepoStore(), &fakeRepoCreator{templatesErr: fmt.Errorf("github down")})
	wErr := doReq(hErr, http.MethodGet, "/api/repo/templates", nil, "alice", "tok", (*Handlers).RepoTemplates)
	require.Equal(t, http.StatusOK, wErr.Code, "fail-soft must still 200")
	var fs RepoTemplatesResponse
	require.NoError(t, json.Unmarshal(wErr.Body.Bytes(), &fs))
	assert.Empty(t, fs.Templates)
}
