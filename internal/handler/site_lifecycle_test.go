package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reservedRegistry struct {
	RegistryWriter
	reserved    sitekey.Slug
	undeleted   []sitekey.Slug
	undeleteErr error
}

func (rr *reservedRegistry) Register(ctx context.Context, slug sitekey.Slug, teams []string, by string) (registry.Site, error) {
	if slug == rr.reserved {
		return registry.Site{}, registry.ErrReserved
	}
	return rr.RegistryWriter.Register(ctx, slug, teams, by)
}

func (rr *reservedRegistry) Undelete(_ context.Context, slug sitekey.Slug) (registry.Reservation, error) {
	rr.undeleted = append(rr.undeleted, slug)
	if rr.undeleteErr != nil {
		return registry.Reservation{}, rr.undeleteErr
	}
	return registry.Reservation{Slug: slug}, nil
}

func TestSiteRegister_RefusesAReservedNameForAStaffCaller(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	h.Registry = &reservedRegistry{RegistryWriter: h.Registry, reserved: "taken"}

	body, _ := json.Marshal(SiteRegisterRequest{Slug: "taken", Teams: []string{"staff"}})
	w := callRegister(h, body, "alice", "tok")

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "site_reserved",
		"a reserved name must not read as a generic upstream failure; the caller can act on the difference")
}

func TestSiteUndelete_RestoresAReservedName(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	rr := &reservedRegistry{RegistryWriter: h.Registry}
	h.Registry = rr
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	fa := &fakeAudit{}
	h.Audit = fa

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, rr.undeleted, 1, "the grace period promises a way back; without one it promises nothing")
	assert.Equal(t, sitekey.Slug("www"), rr.undeleted[0])
	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.undelete", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteDelete_PurgeFlagNoLongerReclaims(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/d/index.html"] = []byte("hi")
	h, res, _ := reserveHandlers(t, store)

	w := callDelete(h, "www?purge=true", "alice", "tok")

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Len(t, res.calls, 1,
		"?purge=true is retired: it must reserve like any delete, never skip the grace period")
	assert.Contains(t, store.objects, "www.freecode.camp/deploys/d/index.html",
		"the destructive reading of the flag fails closed; bytes survive until the reclaim")
}

func TestSiteUndelete_AuditsTheFailureWhenTheDeadlineHasPassed(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	rr := &reservedRegistry{RegistryWriter: h.Registry, undeleteErr: registry.ErrNotFound}
	h.Registry = rr
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	fa := &fakeAudit{}
	h.Audit = fa

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Len(t, fa.events, 1,
		"a refused undelete is a staff action that changed nothing, and the trail must say which")
	assert.Equal(t, "site.undelete", fa.events[0].Action)
	assert.Equal(t, "failure", fa.events[0].Outcome)
}
