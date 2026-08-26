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

func TestSiteDelete_RefusesEveryTrueishSpellingOfTheRetiredFlag(t *testing.T) {
	for _, q := range []string{
		"purge=true", "purge=1", "purge=TRUE", "purge=True", "purge=t", "purge=yes", "purge=on", "purge=",
		"purge=false&purge=true", "purge=true&purge=false", "purge=0&purge=yes",
	} {
		t.Run(q, func(t *testing.T) {
			store := newFakeR2()
			store.aliases["www/production"] = "20260420-141522-abc1234"
			h, res, _ := reserveHandlers(t, store)

			w := callDeleteWithQuery(h, "www", q, "alice", "tok")

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String(),
				"matching only the exact string \"true\" leaves the silent 204 open for every other spelling a caller might send")
			assert.Empty(t, res.calls, "a refused request must take no action")
			assert.Contains(t, store.aliases, "www/production", "a refused delete must not take the site dark")
		})
	}
}

func TestSiteDelete_AnExplicitlyFalsePurgeFlagIsAnOrdinaryDelete(t *testing.T) {
	for _, q := range []string{"purge=false", "purge=0", "purge=FALSE", "purge=f"} {
		t.Run(q, func(t *testing.T) {
			h, res, _ := reserveHandlers(t, newFakeR2())

			w := callDeleteWithQuery(h, "www", q, "alice", "tok")

			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			require.Len(t, res.calls, 1,
				"an explicit false is not the retired flag; the delete must still reserve the name")
		})
	}
}

func TestSiteDelete_AnUnrelatedQueryParamIsAnOrdinaryDelete(t *testing.T) {
	h, res, _ := reserveHandlers(t, newFakeR2())

	w := callDeleteWithQuery(h, "www", "reason=cleanup", "alice", "tok")

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Len(t, res.calls, 1)
}

func TestSiteDelete_LeavesEveryDeployByteInPlace(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.aliases["www/production"] = "20260420-141522-abc1234"
	h, res, _ := reserveHandlers(t, store)

	w := callDelete(h, "www", "alice", "tok")

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Len(t, res.calls, 1)

	store.mu.Lock()
	_, stillThere := store.objects["www/deploys/20260420-141522-abc1234/index.html"]
	store.mu.Unlock()
	assert.True(t, stillThere,
		"ADR-0006's headline invariant: delete unpublishes, it does not reclaim. Moving the deploy prefix here would destroy the bytes undelete restores, inside the grace, with the caller told 204")
	assert.NotContains(t, store.aliases, "www/production",
		"the alias objects are the only thing a delete removes; leaving them serving is the pre-1.10.0 orphan bug")
}
