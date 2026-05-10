package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callUpdate routes a PATCH /api/site/{slug} through a real chi router
// so chi.URLParam resolves the path variable. Without the router, the
// handler reads "" and trips the slug-validation branch.
func callUpdate(h *Handlers, slug string, body []byte, login, token string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Patch("/api/site/{slug}", h.SiteUpdate)

	target := "/api/site/" + slug
	req := httptest.NewRequest(http.MethodPatch, target, bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSiteUpdate_HappyPath(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	// Seed an existing site via Register.
	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	updBody, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"news-editors", "platform"}})
	w := callUpdate(h, "example", updBody, "alice", "tok")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got SiteRow
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []string{"news-editors", "platform"}, got.Teams)
	assert.Equal(t, "alice", got.CreatedBy, "created_by must round-trip")
}

func TestSiteUpdate_404OnAbsentSlug(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"staff"}})
	w := callUpdate(h, "absent", body, "alice", "tok")

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "not_found")
}

func TestSiteUpdate_RejectsCallerNotInAuthzTeam(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {"some-other": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"staff"}})
	w := callUpdate(h, "example", body, "carol", "tok")

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestSiteUpdate_400OnEmptyTeams(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	for _, body := range [][]byte{
		[]byte(`{"teams":[]}`),
		[]byte(`{}`),
	} {
		w := callUpdate(h, "example", body, "alice", "tok")
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "invalid_team")
	}
}

func TestSiteUpdate_400OnInvalidTeam(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"Bad Team"}})
	w := callUpdate(h, "example", body, "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid_team")
}

func TestSiteUpdate_400OnInvalidSlug(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"staff"}})
	w := callUpdate(h, "Bad-Slug", body, "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid_slug")
}

func TestSiteUpdate_400OnMalformedJSON(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	w := callUpdate(h, "example", []byte("not json"), "alice", "tok")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "bad_request")
}

func TestSiteUpdate_502OnRegistryWriteError(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Registry = &erroringRegistry{err: errors.New("kaboom")}

	body, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"staff"}})
	w := callUpdate(h, "example", body, "alice", "tok")

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "registry_write_failed")
}
