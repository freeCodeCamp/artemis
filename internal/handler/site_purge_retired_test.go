package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callDeleteWithQuery(h *Handlers, slug, query, login, token string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/api/site/{slug}", h.SiteDelete)

	req := httptest.NewRequest(http.MethodDelete, "/api/site/"+slug+"?"+query, nil).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSiteDelete_RefusesTheRetiredPurgeFlag(t *testing.T) {
	fa := &fakeAudit{}
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Reservations = &fakeReservations{}
	h.Audit = fa

	w := callDeleteWithQuery(h, "www", "purge=true", "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String(),
		"a silent 204 tells the caller the bytes are gone while they sit untouched at the origin prefix")

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "purge_retired", body.Error.Code)
	assert.Contains(t, body.Error.Message, "/release",
		"the refusal must name the replacement, or the caller has no way to reclaim the bytes")

	assert.Empty(t, fa.events, "a refused request must not write an audit row claiming work happened")
}

func TestSiteDelete_IgnoresAFalsePurgeFlag(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	h.Reservations = &fakeReservations{}

	w := callDeleteWithQuery(h, "www", "purge=false", "alice", "tok")

	assert.NotEqual(t, http.StatusBadRequest, w.Code,
		"only the retired flag is refused; an ordinary delete carrying unrelated query params still works")
}
