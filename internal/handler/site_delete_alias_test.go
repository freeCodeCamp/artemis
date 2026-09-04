package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestSiteDelete_AnswersBadGatewayWhenAnAliasDeleteFails(t *testing.T) {
	r2 := newFakeR2()
	r2.deleteAliasFail = map[string]error{"example/preview": errors.New("r2 down")}
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, r2)
	h.Reservations = &fakeReservations{}
	registerExample(t, h)

	w := callDelete(h, "example", "alice", "tok")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "r2_delete_failed",
		"a half-unpublished site must report the failure, not a clean 204")
}

func TestSiteDelete_CommitsWhenTheClientDisconnects(t *testing.T) {
	r2 := newFakeR2()
	h, _ := newTestHandlers(t, staffCallerGH(), &fakeSites{bySite: map[sitekey.Slug][]string{}}, r2)
	h.Reservations = &fakeReservations{}
	registerExample(t, h)
	r2.aliases["example/production"] = "20260420-141522-abc1234"

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	router := chi.NewRouter()
	router.Delete("/api/site/{slug}", h.SiteDelete)
	req := httptest.NewRequest(http.MethodDelete, "/api/site/example", nil).WithContext(ctx)
	router.ServeHTTP(httptest.NewRecorder(), req)

	_, still := r2.aliases["example/production"]
	require.False(t, still, "the takedown commits on a context detached from the client")
}
