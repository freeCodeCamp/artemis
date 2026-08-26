package handler

import (
	"context"
	"encoding/json"
	"errors"
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
	readErr     error
	reservation registry.Reservation
}

func (rr *reservedRegistry) Register(ctx context.Context, slug sitekey.Slug, teams []string, by string) (registry.Site, error) {
	if slug == rr.reserved {
		return registry.Site{}, registry.ErrReserved
	}
	return rr.RegistryWriter.Register(ctx, slug, teams, by)
}

func (rr *reservedRegistry) Reservation(_ context.Context, slug sitekey.Slug) (registry.Reservation, error) {
	if rr.readErr != nil {
		return registry.Reservation{}, rr.readErr
	}
	res := rr.reservation
	res.Slug = slug
	return res, nil
}

func (rr *reservedRegistry) Undelete(_ context.Context, slug sitekey.Slug) (registry.Reservation, error) {
	rr.undeleted = append(rr.undeleted, slug)
	if rr.undeleteErr != nil {
		return registry.Reservation{}, rr.undeleteErr
	}
	res := rr.reservation
	res.Slug = slug
	return res, nil
}

func TestSiteUndelete_RestoresTheAliasObjectsThatPinTheDeploys(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	rr := &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
		PrevPreview:    "20260421-090000-def5678",
	}}
	h.Registry = rr
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	idx := &fakeIndex{}
	h.Index = idx

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "20260420-141522-abc1234", store.aliases["www/production"],
		"gc-site pins a deploy only when an R2 alias names it, so an undelete that restores no alias hands the rescued deploy to the collector")
	assert.Equal(t, "20260421-090000-def5678", store.aliases["www/preview"],
		"the preview pointer was captured at reserve time for the same reason production was")
	assert.Equal(t, []string{
		"www/production/20260420-141522-abc1234",
		"www/preview/20260421-090000-def5678",
	}, idx.aliased,
		"the aliases rows feed Store.AliasTargets and gc.PlanSite; restoring only the R2 half leaves the pg view still empty")
}

func TestSiteUndelete_ReportsTheStageWhenTheIndexHalfFails(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	rr := &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
	}}
	h.Registry = rr
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	fa := &fakeAudit{}
	h.Audit = fa
	h.Index = &fakeIndex{fail: true}

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Empty(t, rr.undeleted, "the row must stay reserved so the caller can retry inside the grace")
	require.Len(t, fa.events, 1)
	assert.Equal(t, "restore_index", fa.events[0].Detail["stage"],
		"the three failure stages leave different residues; without the stage the operator cannot tell which one happened")
}

func TestSiteUndelete_KeepsTheNameReservedWhenTheAliasRestoreFails(t *testing.T) {
	store := newFakeR2()
	store.putErr = errors.New("r2 down")
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	rr := &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
	}}
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

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "r2_put_failed",
		"an R2 write failure reported as registry_write_failed sends the operator to the wrong subsystem")
	assert.Empty(t, rr.undeleted,
		"Undelete clears prev_production in the same statement that returns it, so flipping the row before the pins are back loses the pointers for good")
	require.Len(t, fa.events, 1)
	assert.Equal(t, "restore_alias", fa.events[0].Detail["stage"])
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

func TestSiteDelete_PurgeFlagIsRefusedNotSilentlyIgnored(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/d/index.html"] = []byte("hi")
	h, res, _ := reserveHandlers(t, store)

	w := callDelete(h, "www?purge=true", "alice", "tok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String(),
		"answering 204 tells a takedown script the bytes are gone while they sit at the origin prefix")
	assert.Empty(t, res.calls,
		"an ambiguous destructive request takes no action at all; the site stays live and the caller is told why")
	assert.Contains(t, store.objects, "www.freecode.camp/deploys/d/index.html")
	assert.Contains(t, store.aliases, "www/production",
		"a refused delete must not take the site dark either")
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

func TestToSiteRow_CarriesTheReservedStateAndDeadline(t *testing.T) {
	until := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	row := toSiteRow(registry.Site{Slug: "held", State: registry.StateReserved, ReservedUntil: until})

	require.NotNil(t, row.ReservedUntil,
		"universe sites ls shows a held name as live unless the deadline reaches the caller")
	assert.Equal(t, registry.StateReserved, row.State)
	assert.Equal(t, until, *row.ReservedUntil)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"state":"reserved"`)
	assert.Contains(t, string(b), `"reservedUntil":"2026-08-29T03:00:00Z"`)
}

func TestToSiteRow_DefaultsAStatelessStoreToActiveAndOmitsTheDeadline(t *testing.T) {
	row := toSiteRow(registry.Site{Slug: "live"})

	assert.Equal(t, registry.StateActive, row.State,
		"a store with no reservation concept holds an active site; omitting the field would leave a client unable to tell absent from unknown")
	assert.Nil(t, row.ReservedUntil)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "reservedUntil")
}
