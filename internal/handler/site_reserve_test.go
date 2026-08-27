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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReservations struct {
	calls    []sitekey.Slug
	until    []time.Time
	by       []string
	observed []registry.ObservedAliases
	err      error
	// reg mirrors internal/pg/reservation.go:24 — Reserve locks the sites
	// row first and answers ErrNotFound when there is none.
	reg *fakeRegistry
}

func (f *fakeReservations) Reserve(ctx context.Context, slug sitekey.Slug, _ sitekey.Dirname,
	until time.Time, by string, observed registry.ObservedAliases) (registry.Reservation, error) {
	if f.reg != nil {
		if _, err := f.reg.GetSite(ctx, slug); err != nil {
			return registry.Reservation{}, err
		}
	}
	f.calls = append(f.calls, slug)
	f.until = append(f.until, until)
	f.by = append(f.by, by)
	f.observed = append(f.observed, observed)
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
	if reg, ok := h.Registry.(*fakeRegistry); ok {
		res.reg = reg
	}
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

func TestSiteDelete_OrphanedAliasWithNoRegistryRowUnpublishesAndSaysSo(t *testing.T) {
	store := newFakeR2()
	store.aliases["ghost/production"] = "20260420-141522-abc1234"
	store.aliases["ghost/preview"] = "20260420-141522-abc1234"
	h, res, fa := reserveHandlers(t, store)

	w := callDelete(h, "ghost", "alice", "tok")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, store.aliases, "ghost/production",
		"the orphan is only cleared once the serve plane stops answering")
	assert.NotContains(t, store.aliases, "ghost/preview")
	assert.Empty(t, res.calls, "there is no row to reserve, so no name is held")

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "unpublished", body["status"])
	assert.Equal(t, false, body["reserved"],
		"the caller must learn undelete cannot bring this one back")

	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.delete", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome,
		"the audit trail must name what the call did, not report a failure it did not have")
}

func TestSiteDelete_OrphanedAliasDeleteIsIdempotent(t *testing.T) {
	store := newFakeR2()
	store.aliases["ghost/production"] = "20260420-141522-abc1234"
	h, _, _ := reserveHandlers(t, store)

	require.Equal(t, http.StatusOK, callDelete(h, "ghost", "alice", "tok").Code)

	w := callDelete(h, "ghost", "alice", "tok")
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "not_found",
		"once the orphan is gone the name is absent, and absent is the 404 the matrix documents")
}

type aliasReadFailR2 struct {
	*fakeR2
	failKey string
}

func (d *aliasReadFailR2) GetAlias(ctx context.Context, key string) (string, error) {
	if key == d.failKey {
		return "", errors.New("r2 alias read timeout")
	}
	return d.fakeR2.GetAlias(ctx, key)
}

func TestSiteDelete_AnUnreadableAliasProbeIsUnknownNotAbsent(t *testing.T) {
	inner := newFakeR2()
	inner.aliases["ghost/production"] = "20260420-141522-abc1234"
	store := &aliasReadFailR2{fakeR2: inner, failKey: "ghost/production"}
	h, _, fa := reserveHandlers(t, store)

	w := callDelete(h, "ghost", "alice", "tok")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, inner.aliases, "ghost/production")
	require.Len(t, fa.events, 1)
	assert.Equal(t, "success", fa.events[0].Outcome,
		"a probe that could not read is unknown, not zero; 404 would assert nothing was served")
	assert.Equal(t, false, fa.events[0].Detail["orphan"],
		"an unreadable alias read cannot witness an orphan, so the audit row must not claim one was cleaned up")
	assert.Equal(t, "unreadable", fa.events[0].Detail["aliasProbe"],
		"the operator needs the uncertainty on the record, not folded into a success that reads as observed")
}

func TestSiteDelete_ReadsTheLiveAliasPointerBeforeDeletingIt(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260827-140000-newsha"
	store.aliases["www/preview"] = "20260801-090000-oldsha"
	h, res, _ := reserveHandlers(t, store)
	log := &eventLog{}
	h.R2 = &loggingR2{fakeR2: store, log: log}

	require.Equal(t, http.StatusNoContent, callDeleteSite(t, h).Code)

	assert.Equal(t, []string{
		"getAlias:www/production", "deleteAlias:www/production",
		"getAlias:www/preview", "deleteAlias:www/preview",
	}, log.events,
		"the serve plane reads the R2 alias object, so it is the live pointer; a HEAD discards the one value undelete needs and the delete then destroys the only copy")
	require.Len(t, res.observed, 1)
	require.NotNil(t, res.observed[0].Production)
	assert.Equal(t, "20260827-140000-newsha", *res.observed[0].Production,
		"prev_production must record what R2 served, not what the aliases table remembered")
	require.NotNil(t, res.observed[0].Preview)
	assert.Equal(t, "20260801-090000-oldsha", *res.observed[0].Preview)
}

func TestSiteDelete_LeavesThePointerUnknownWhenR2CannotBeRead(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/production"] = "20260827-140000-newsha"
	h, res, _ := reserveHandlers(t, &aliasReadFailR2{fakeR2: store, failKey: "www/production"})

	require.Equal(t, http.StatusNoContent, callDeleteSite(t, h).Code)

	require.Len(t, res.observed, 1)
	assert.Nil(t, res.observed[0].Production,
		"an unreadable alias must fall back to the aliases table, not overwrite prev_production with an empty string")
}
