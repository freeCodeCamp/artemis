package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReleaser struct {
	released  []sitekey.Slug
	expired   []sitekey.Slug
	err       error
	expireErr error
	log       *eventLog
}

func (f *fakeReleaser) ReleaseReservationNow(_ context.Context, slug sitekey.Slug) error {
	f.released = append(f.released, slug)
	return f.err
}

func (f *fakeReleaser) ExpireReservation(_ context.Context, slug sitekey.Slug) error {
	if f.log != nil {
		f.log.add("expire:" + string(slug))
	}
	if f.expireErr != nil {
		return f.expireErr
	}
	f.expired = append(f.expired, slug)
	return nil
}

func releaseHandlers(t *testing.T, repoGH *fakeGH, store R2Store) (*Handlers, *fakeReleaser, *fakeTombstones, *fakeAudit) {
	t.Helper()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	rel := &fakeReleaser{}
	tomb := &fakeTombstones{}
	fa := &fakeAudit{}
	h.RepoGH = repoGH
	h.RepoApproveAuthzTeam = "apollo-11-approvers"
	h.NameReleaser = rel
	h.Tombstones = tomb
	h.Audit = fa
	return h, rel, tomb, fa
}

func callRelease(h *Handlers, slug, login, token string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Post("/api/site/{slug}/release", h.SiteRelease)
	req := httptest.NewRequest(http.MethodPost, "/api/site/"+slug+"/release", nil).
		WithContext(contextWithLogin(context.Background(), login, token))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSiteRelease_ApproverFreesTheNameAndReclaimsTheBytes(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, tomb, fa := releaseHandlers(t, adminRepoGH(), store)
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))

	w := callRelease(h, "www", "boss", "atok")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, rel.released, 1, "an approver who cannot free the name has no path but psql")
	assert.Equal(t, sitekey.Slug("www"), rel.released[0])
	assert.Equal(t, []string{"www"}, tomb.purged,
		"the tombstone must land before the bytes move, so tombstone-purge owns them")
	assert.NotContains(t, store.objects, "www/deploys/d1/index.html")
	assert.Contains(t, store.objects, "_trash/www/deploys/d1/index.html")

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "released", body["status"])

	require.Len(t, fa.events, 1)
	assert.Equal(t, "site.release", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSiteRelease_RefusesACallerWhoMayOnlyDelete(t *testing.T) {
	h, rel, _, _ := releaseHandlers(t, staffRepoGH(), newFakeR2())

	w := callRelease(h, "www", "alice", "tok")

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Empty(t, rel.released,
		"REGISTRY_AUTHZ_TEAM may delete reversibly; only REPO_APPROVE_AUTHZ_TEAM may destroy early")
}

func TestSiteRelease_RefusesAnActiveSiteBeforeTouchingAnyBytes(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, tomb, fa := releaseHandlers(t, adminRepoGH(), store)

	w := callRelease(h, "www", "boss", "atok")

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Empty(t, tomb.purged,
		"an active site is not releasable; reaching the bytes at all would destroy a live site")
	assert.Contains(t, store.objects, "www/deploys/d1/index.html")
	assert.Empty(t, rel.released, "the name of a live site must never be freed")
	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
}

func TestSiteRelease_FreesTheNameOnlyAfterTheBytesAreTrashed(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, _, _ := releaseHandlers(t, adminRepoGH(), store)
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))
	h.R2 = &movePrefixFailR2{fakeR2: store}

	w := callRelease(h, "www", "boss", "atok")

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Empty(t, rel.released,
		"SiteRegister takes no site lock, so a name freed before its bytes move lets the next "+
			"claimant register and then have their own upload swept into _trash")
}

func TestSiteRelease_400OnInvalidSlug(t *testing.T) {
	h, _, _, _ := releaseHandlers(t, adminRepoGH(), newFakeR2())

	w := callRelease(h, "Bad-Slug", "boss", "atok")

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

type movePrefixFailR2 struct {
	*fakeR2
}

func (movePrefixFailR2) MovePrefix(context.Context, string, string) (int, error) {
	return 0, errors.New("r2 down")
}

func TestSiteRelease_HoldsTheSiteLockAcrossTheWholeReclaim(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, _, _, _ := releaseHandlers(t, adminRepoGH(), store)
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))
	log := &eventLog{}
	h.Locker = &fakeLocker{log: log}
	h.R2 = &loggingR2{fakeR2: store, log: log}

	require.Equal(t, http.StatusOK, callRelease(h, "www", "boss", "atok").Code)

	assert.Equal(t, []string{"lock:www", "move:www/", "unlock:www"}, log.events,
		"the reclaim must run inside the lock; a move outside it races the sweep and undelete")
}

func TestSiteUndelete_TakesTheSameLockSiteReleaseHolds(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	rr := &reservedRegistry{RegistryWriter: h.Registry}
	h.Registry = rr
	h.Audit = &fakeAudit{}
	log := &eventLog{}
	h.Locker = &fakeLocker{log: log}

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"lock:www", "unlock:www"}, log.events,
		"undelete must share SiteRelease's key, or it can return an emptied site mid-reclaim")
}

func TestSiteRelease_DisarmsUndeleteBeforeItMovesAnyBytes(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, _, _ := releaseHandlers(t, adminRepoGH(), store)
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))
	log := &eventLog{}
	rel.log = log
	h.Locker = &fakeLocker{log: log}
	h.R2 = &loggingR2{fakeR2: store, log: log}

	require.Equal(t, http.StatusOK, callRelease(h, "www", "boss", "atok").Code)

	assert.Equal(t, []string{"lock:www", "expire:www", "move:www/", "unlock:www"}, log.events,
		"every post-move failure path returns 502 leaving the row reserved with a future deadline, "+
			"and undelete needs only that deadline to restore alias pins onto bytes already in _trash")
	assert.Equal(t, []sitekey.Slug{"www"}, rel.expired)
}

func TestSiteRelease_KeepsTheNameReservedButUndeletableWhenTheMoveFails(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, _, _ := releaseHandlers(t, adminRepoGH(), movePrefixFailR2{store})
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))

	w := callRelease(h, "www", "boss", "atok")

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Empty(t, rel.released, "the row stays state=reserved, which blocks re-registration and makes the call safe to retry")
	assert.Equal(t, []sitekey.Slug{"www"}, rel.expired,
		"a partially moved site must not stay undeletable-armed; the expired row degrades to the sweep's already-safe shape")
}

func TestSiteRelease_RefusesToTouchBytesWhenItCannotDisarmUndelete(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/d1/index.html"] = []byte("hi")
	h, rel, tomb, _ := releaseHandlers(t, adminRepoGH(), store)
	reserveSite(h, h.Registry.(*fakeRegistry), "www", time.Now().Add(72*time.Hour))
	rel.expireErr = errors.New("pg down")

	w := callRelease(h, "www", "boss", "atok")

	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Empty(t, tomb.purged, "no tombstone may be written for a site whose bytes were never moved")
	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Contains(t, store.objects, "www/deploys/d1/index.html",
		"failing to disarm undelete must abort before the first destructive step, not after it")
}
