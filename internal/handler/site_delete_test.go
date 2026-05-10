package handler

import (
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

// callDelete routes a DELETE /api/site/{slug} through a real chi router
// so chi.URLParam resolves the path variable.
func callDelete(h *Handlers, slug, login, token string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/api/site/{slug}", h.SiteDelete)

	target := "/api/site/" + slug
	req := httptest.NewRequest(http.MethodDelete, target, nil).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSiteDelete_HappyPath(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	w := callDelete(h, "example", "alice", "tok")
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	// Subsequent list omits the slug.
	listW := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, listW.Code)
	var rows []SiteRow
	require.NoError(t, json.Unmarshal(listW.Body.Bytes(), &rows))
	assert.Empty(t, rows)
}

func TestSiteDelete_404OnAbsentSlug(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	w := callDelete(h, "absent", "alice", "tok")
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "not_found")
}

func TestSiteDelete_RejectsCallerNotInAuthzTeam(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {"some-other": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	w := callDelete(h, "example", "carol", "tok")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestSiteDelete_400OnInvalidSlug(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	w := callDelete(h, "Bad-Slug", "alice", "tok")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid_slug")
}

func TestSiteDelete_502OnRegistryWriteError(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Registry = &erroringRegistry{err: errors.New("kaboom")}

	w := callDelete(h, "example", "alice", "tok")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "registry_write_failed")
}
