package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// callRegister POSTs the given body to SiteRegister with the test
// context (login + GH token attached). Returns the response recorder.
func callRegister(h *Handlers, body []byte, login, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/site/register", bytes.NewReader(body)).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	h.SiteRegister(w, r)
	return w
}

// staffCallerGH wires a fakeGH where `tok` resolves to `alice` and
// alice is on the `staff` team. Most happy-path tests use this.
func staffCallerGH() *fakeGH {
	return &fakeGH{
		tokenLogins: map[string]string{"tok": "alice"},
		userTeams:   map[string]map[string]bool{"alice": {"staff": true}},
	}
}

func TestSiteRegister_HappyPath(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteRegisterRequest{
		Slug:  "example",
		Teams: []string{"staff", "platform"},
	})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var got SiteRegisterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "example", got.Slug)
	assert.Equal(t, []string{"staff", "platform"}, got.Teams)
	assert.Equal(t, "alice", got.CreatedBy)
	assert.False(t, got.CreatedAt.IsZero())
	assert.True(t, got.CreatedAt.Equal(got.UpdatedAt))
}

func TestSiteRegister_DefaultsToAuthzTeamWhenTeamsEmpty(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"missing teams field", []byte(`{"slug":"example"}`)},
		{"empty teams array", []byte(`{"slug":"example","teams":[]}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
			w := callRegister(h, tc.body, "alice", "tok")

			require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

			var got SiteRegisterResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, []string{"staff"}, got.Teams)
		})
	}
}

func TestSiteRegister_RejectsCallerNotInAuthzTeam(t *testing.T) {
	gh := &fakeGH{
		tokenLogins: map[string]string{"tok": "carol"},
		userTeams:   map[string]map[string]bool{"carol": {"some-other-team": true}},
	}
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "example"})
	w := callRegister(h, body, "carol", "tok")

	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestSiteRegister_409OnDuplicateSlug(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "example"})

	w1 := callRegister(h, body, "alice", "tok")
	require.Equal(t, http.StatusCreated, w1.Code, w1.Body.String())

	w2 := callRegister(h, body, "alice", "tok")
	require.Equal(t, http.StatusConflict, w2.Code, w2.Body.String())
	assert.Contains(t, w2.Body.String(), "already_exists")
}

func TestSiteRegister_400OnInvalidSlug(t *testing.T) {
	cases := []struct {
		name string
		slug string
	}{
		{"empty", ""},
		{"uppercase", "Example"},
		{"underscore", "ex_ample"},
		{"leading digit", "1example"},
		{"leading hyphen", "-example"},
		{"too long", "a234567890123456789012345678901234567890123456789012345678901234"}, // 64 chars
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
			body, _ := json.Marshal(SiteRegisterRequest{Slug: tc.slug})
			w := callRegister(h, body, "alice", "tok")
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), "invalid_slug")
		})
	}
}

func TestSiteRegister_400OnInvalidTeam(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	body, _ := json.Marshal(SiteRegisterRequest{
		Slug:  "example",
		Teams: []string{"Bad Team"},
	})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid_team")
}

func TestSiteRegister_400OnMalformedJSON(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	w := callRegister(h, []byte("not json"), "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "bad_request")
}

func TestSiteRegister_502OnRegistryWriteError(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())

	// Inject a transient registry error.
	fr := h.Registry.(*fakeRegistry)
	fr.registerErr = errors.New("kaboom")

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "example"})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "registry_write_failed")
}

func TestSiteRegister_503OnUpstreamAuthzProbeError(t *testing.T) {
	gh := staffCallerGH()
	gh.upstreamErr = errors.New("upstream unavailable")
	h, _ := newTestHandlers(t, gh, standardSites(), newFakeR2())

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "example"})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

func TestSiteRegister_500WhenAuthzTeamMisconfigured(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.RegistryAuthzTeam = ""

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "example"})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
}
