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

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakePurger struct {
	calls  [][]string
	ctxErr error
	err    error
}

func (p *fakePurger) PurgeHosts(ctx context.Context, hosts []string) error {
	p.calls = append(p.calls, hosts)
	p.ctxErr = ctx.Err()
	return p.err
}

func registerForPurgeTest(t *testing.T, h *Handlers) {
	t.Helper()
	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)
}

func TestSiteDelete_PurgesBothPublicHosts(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	purger := &fakePurger{}
	h.EdgePurge = purger
	h.Reservations = &fakeReservations{}
	registerForPurgeTest(t, h)

	require.Equal(t, http.StatusNoContent, callDelete(h, "example", "alice", "tok").Code)

	require.Len(t, purger.calls, 1, "one purge call carries every host, matching the Cloudflare purge API")
	assert.ElementsMatch(t,
		[]string{"example.freecode.camp", "example.preview.freecode.camp"},
		purger.calls[0],
		"removing the alias only stops the origin; Cloudflare keeps serving assets from its edge until purged")
}

func TestSiteDelete_SucceedsWhenThePurgeFails(t *testing.T) {
	logs := captureAccessLog(t)
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	h.EdgePurge = &fakePurger{err: errors.New("cloudflare unreachable")}
	h.Reservations = &fakeReservations{}
	registerForPurgeTest(t, h)

	require.Equal(t, http.StatusNoContent, callDelete(h, "example", "alice", "tok").Code,
		"the aliases are already gone under the site lock, so the takedown is authoritative; failing the "+
			"request would report a rollback that did not happen")
	assert.Equal(t, 1, logs.countMessage("edge.purge.failed"),
		"best-effort only holds if the failure is loud; a silent skip leaves assets serving with nobody told")
}

func TestSiteDelete_WorksWithoutAPurgerWired(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	h.EdgePurge = nil
	h.Reservations = &fakeReservations{}
	registerForPurgeTest(t, h)

	require.Equal(t, http.StatusNoContent, callDelete(h, "example", "alice", "tok").Code,
		"edge purging is optional wiring; its absence must not break a takedown")
}

func TestSiteDelete_PurgesWhatWasDeletedWhenTheSecondAliasFails(t *testing.T) {
	r2 := newFakeR2()
	r2.deleteAliasFail = map[string]error{"example/preview": errors.New("r2 down")}
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, r2)
	purger := &fakePurger{}
	h.EdgePurge = purger
	h.Reservations = &fakeReservations{}
	registerForPurgeTest(t, h)

	require.Equal(t, http.StatusBadGateway, callDelete(h, "example", "alice", "tok").Code)

	require.Len(t, purger.calls, 1,
		"production's alias is already gone, so leaving it cached is the harm the purge exists to prevent")
	assert.Equal(t, []string{"example.freecode.camp"}, purger.calls[0],
		"only the host whose alias actually went may be purged")
}

func TestSiteDelete_PurgesEvenWhenTheClientDisconnects(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	purger := &fakePurger{}
	h.EdgePurge = purger
	h.Reservations = &fakeReservations{}
	registerForPurgeTest(t, h)

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	router := chi.NewRouter()
	router.Delete("/api/site/{slug}", h.SiteDelete)
	req := httptest.NewRequest(http.MethodDelete, "/api/site/example", nil).WithContext(ctx)
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Len(t, purger.calls, 1,
		"the takedown commits on a context detached from the client, so the purge must not be skipped by a disconnect")
	require.NoError(t, purger.ctxErr, "a cancelled context would make the real client send no request at all")
}

func TestSiteDelete_RefusesToPurgeAHostThatIsNotTheSitesOwn(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, newFakeR2())
	purger := &fakePurger{}
	h.EdgePurge = purger
	h.Reservations = &fakeReservations{}
	// config.Validate accepts a path-based format, which collapses every site onto the apex host.
	h.PublicProductionURLFmt = "https://freecode.camp/sites/<site>"
	h.PublicPreviewURLFmt = "https://freecode.camp/preview/<site>"
	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "code", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	require.Equal(t, http.StatusNoContent, callDelete(h, "code", "alice", "tok").Code)

	assert.Empty(t, purger.calls,
		"the slug `code` is a substring of `freecode.camp`, so a containment check would purge the shared "+
			"apex host and wipe every other site's cache on one takedown")
}
