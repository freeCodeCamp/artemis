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
)

func callSitesList(h *Handlers, login, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/sites", nil).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	h.SitesList(w, r)
	return w
}

func TestSitesList_EmptyRegistry(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	w := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got)
}

func TestSitesList_PopulatedReturnsRowsSorted(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	for _, slug := range []string{"charlie", "alpha", "bravo"} {
		body := []byte(`{"slug":"` + slug + `","teams":["staff"]}`)
		w := callRegister(h, body, "alice", "tok")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	}

	h.RepoGH = staffCallerGH()
	h.AuditReadAuthzTeam = "staff"

	w := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, "alpha", got[0].Slug)
	assert.Equal(t, "bravo", got[1].Slug)
	assert.Equal(t, "charlie", got[2].Slug)
	assert.Equal(t, []string{"staff"}, got[0].Teams)
	assert.Equal(t, "alice", got[0].CreatedBy)
	assert.False(t, got[0].CreatedAt.IsZero())
}

func nonStaffGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "mallory"},
		userTeams:   map[string]map[string]bool{"mallory": {"platform": true}},
	}
}

func TestSitesList_RedactsCreatedByForNonStaff(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	body := []byte(`{"slug":"alpha","teams":["staff"]}`)
	require.Equal(t, http.StatusCreated, callRegister(h, body, "alice", "tok").Code)

	h.RepoGH = nonStaffGH()
	h.AuditReadAuthzTeam = "staff"

	w := callSitesList(h, "mallory", "tok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Empty(t, got[0].CreatedBy, "non-staff caller must not see actor identity")
	assert.Equal(t, "alpha", got[0].Slug, "non-actor fields stay visible")
	assert.Equal(t, []string{"staff"}, got[0].Teams)
}

func TestSitesList_RedactsWhenAuthzProbeErrors(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())

	body := []byte(`{"slug":"alpha","teams":["staff"]}`)
	require.Equal(t, http.StatusCreated, callRegister(h, body, "alice", "tok").Code)

	h.RepoGH = &fakeGH{
		tokenLogins:  map[string]string{"tok": "alice"},
		userTeams:    map[string]map[string]bool{"alice": {"staff": true}},
		authorizeErr: errors.New("github probe down"),
	}
	h.AuditReadAuthzTeam = "staff"

	w := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, w.Code, "an authz-probe error must not 500 the list")

	var got []SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Empty(t, got[0].CreatedBy, "fail closed: probe error must redact, never leak")
}

func TestSitesList_502OnRegistryReadError(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	// Inject error into the fake by replacing with a sentinel-error
	// version. Using a small wrapper rather than touching fakeRegistry
	// directly keeps the existing happy-path tests stable.
	h.Registry = &erroringRegistry{err: errors.New("kaboom")}

	w := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "registry_read_failed")
}

func TestSitesList_ActorGateIndependentOfRepoFeature(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[string][]string{}}, newFakeR2())
	h.RepoGH = staffCallerGH()
	h.AuditReadAuthzTeam = "staff"
	require.False(t, h.RepoEnabled(), "repo-create feature off (Repos/GitHubApp nil) — actor/audit gating must not depend on it")

	body := []byte(`{"slug":"alpha","teams":["staff"]}`)
	require.Equal(t, http.StatusCreated, callRegister(h, body, "alice", "tok").Code)

	w := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got []SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, "alice", got[0].CreatedBy, "staff sees actor identity even when the repo-create feature is disabled")
}
