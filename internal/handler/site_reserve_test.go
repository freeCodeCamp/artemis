package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReservations struct {
	calls []sitekey.Slug
	until []time.Time
	by    []string
	err   error
}

func (f *fakeReservations) Reserve(_ context.Context, slug sitekey.Slug, _ sitekey.Dirname,
	until time.Time, by string) (registry.Reservation, error) {
	f.calls = append(f.calls, slug)
	f.until = append(f.until, until)
	f.by = append(f.by, by)
	if f.err != nil {
		return registry.Reservation{}, f.err
	}
	return registry.Reservation{Slug: slug, ReservedUntil: until, ReservedBy: by}, nil
}

type deleteAliasFailR2 struct {
	*fakeR2
	failKey string
}

func (d *deleteAliasFailR2) DeleteAlias(ctx context.Context, aliasKey string) error {
	if aliasKey == d.failKey {
		return errors.New("r2 down")
	}
	return d.fakeR2.DeleteAlias(ctx, aliasKey)
}

func reserveHandlers(t *testing.T, store R2Store) (*Handlers, *fakeReservations, *fakeAudit) {
	t.Helper()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	res := &fakeReservations{}
	fa := &fakeAudit{}
	h.Reservations = res
	h.ReservationGrace = 72 * time.Hour
	h.Audit = fa
	return h, res, fa
}

func callDeleteSite(t *testing.T, h *Handlers) *httptest.ResponseRecorder {
	t.Helper()
	return callDelete(h, "www", "alice", "tok")
}

func TestSiteDelete_TakesTheSiteDarkThenReservesTheName(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260420-141522-abc1234"
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, res, fa := reserveHandlers(t, store)

	w := callDeleteSite(t, h)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.NotContains(t, store.aliases, "www/production",
		"the serve plane reads r2 aliases directly, so the site is only dark once they are gone")
	assert.NotContains(t, store.aliases, "www/preview")
	require.Len(t, res.calls, 1, "the name must be held, not freed")
	assert.Equal(t, sitekey.Slug("www"), res.calls[0])
	assert.Equal(t, "alice", res.by[0])
	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.delete", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteDelete_AliasFailureAbortsAndLeavesTheSiteRegistered(t *testing.T) {
	base := newFakeR2()
	base.aliases["www/production"] = "20260420-141522-abc1234"
	base.aliases["www/preview"] = "20260420-141522-abc1234"
	store := &deleteAliasFailR2{fakeR2: base, failKey: "www/preview"}
	h, res, fa := reserveHandlers(t, store)

	w := callDeleteSite(t, h)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Empty(t, res.calls,
		"no ordering may produce deregistered-and-still-serving; an alias failure must abort")
	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "unpublish", fa.events[0].Detail["stage"])
}

func TestSiteDelete_ReserveFailureAfterTheUnpublishAuditsItsOwnStage(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260420-141522-abc1234"
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	h, res, fa := reserveHandlers(t, store)
	res.err = errors.New("pg down")

	w := callDeleteSite(t, h)

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "reserve", fa.events[0].Detail["stage"],
		"the site is already dark; only the stage tells an operator whether the name is held")
}

func TestSiteDelete_RepeatingADeleteOverAbsentAliasesStillReachesReserve(t *testing.T) {
	store := newFakeR2()
	h, res, _ := reserveHandlers(t, store)

	w := callDeleteSite(t, h)

	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	require.Len(t, res.calls, 1,
		"a retry after a reserve failure must not stall on aliases the first attempt already removed")
}
