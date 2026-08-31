package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
)

func TestDeployFinalize_PurgesTheHostItJustRepublished(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	purger := &fakePurger{}
	h.EdgePurge = purger

	require.Equal(t, http.StatusOK, callFinalize(t, h, jwt, deployID).Code)

	require.Len(t, purger.calls, 1,
		"finalize is the most frequent alias write of all; without a purge every deploy leaves the "+
			"previous one serving from the edge for the full CDN TTL")
	assert.Equal(t, []string{"www.preview.freecode.camp"}, purger.calls[0],
		"only the mode finalize actually published may be purged")
}

func TestSiteUndelete_PurgesBothRestoredHosts(t *testing.T) {
	store := newFakeR2()
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Registry = &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
		PrevPreview:    "20260421-090000-def5678",
	}}
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	h.Index = &fakeIndex{}
	purger := &fakePurger{}
	h.EdgePurge = purger

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, purger.calls, 1)
	assert.ElementsMatch(t,
		[]string{"www.freecode.camp", "www.preview.freecode.camp"},
		purger.calls[0],
		"the takedown purged these hosts and the edge then cached the dark state; an undelete that "+
			"does not purge republishes into a cache still serving the takedown")
}

func TestSitePromote_PurgesTheProductionHost(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Audit = &fakeAudit{}
	purger := &fakePurger{}
	h.EdgePurge = purger

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, purger.calls, 1)
	assert.Equal(t, []string{"www.freecode.camp"}, purger.calls[0],
		"a promote the staffer cannot see is read as a broken deploy; preview did not move, so it is not purged")
}

func TestSiteRollback_PurgesTheProductionHost(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/20260419-090000-d2/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Audit = &fakeAudit{}
	purger := &fakePurger{}
	h.EdgePurge = purger

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260419-090000-d2", ExpectedCurrent: "d1-current"})
	w := withChiRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteRollback))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, purger.calls, 1)
	assert.Equal(t, []string{"www.freecode.camp"}, purger.calls[0],
		"a rollback is an emergency republish; leaving the bad deploy cached at the edge defeats it")
}

func TestAliasMutations_AllRouteThroughThePurgeSeam(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			name == "aliaswrite.go" || filepath.Base(filepath.Dir(path)) == "r2" {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, ".PutAlias(") || strings.Contains(line, ".DeleteAlias(") {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	}))

	assert.Empty(t, offenders,
		"every alias write must go through putAliasTouched or deleteAliasTouched in aliaswrite.go, "+
			"which records the mode so the handler can purge the edge for it; artemis shipped five alias "+
			"writes and purged one because each call site had to remember on its own. R2Store is an "+
			"exported interface on an exported field, so this walks the whole repository rather than "+
			"one package")
}

func TestDeployFinalize_PurgesEvenWhenTheIndexWriteFails(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, jwt, _ := newFinalizeHandlers(t, store)
	h.Index = &fakeIndex{fail: true}
	purger := &fakePurger{}
	h.EdgePurge = purger

	require.Equal(t, http.StatusBadGateway, callFinalize(t, h, jwt, deployID).Code)

	require.Len(t, purger.calls, 1,
		"the alias already moved in R2 and the serve plane reads the object, not the index row, so the "+
			"deploy IS live; leaving the previous one cached is the harm the purge exists to prevent")
	assert.Equal(t, []string{"www.preview.freecode.camp"}, purger.calls[0])
}

func TestSitePromote_PurgesEvenWhenTheIndexWriteFails(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Audit = &fakeAudit{}
	h.Index = &fakeIndex{fail: true}
	purger := &fakePurger{}
	h.EdgePurge = purger

	w := withChiRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", nil, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusBadGateway, w.Code)

	require.Len(t, purger.calls, 1,
		"the production alias object already points at the new deploy; the failed pg row does not put it back")
}

func TestSiteRollback_PurgesEvenWhenTheIndexWriteFails(t *testing.T) {
	store := newFakeR2()
	store.objects["www/deploys/20260419-090000-d2/index.html"] = []byte("ok")
	store.aliases["www/production"] = "d1-current"
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Audit = &fakeAudit{}
	h.Index = &fakeIndex{fail: true}
	purger := &fakePurger{}
	h.EdgePurge = purger

	body, _ := json.Marshal(SiteRollbackRequest{To: "20260419-090000-d2", ExpectedCurrent: "d1-current"})
	w := withChiRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body, bearerTok(),
		RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SiteRollback))).ServeHTTP,
		context.Background())
	require.Equal(t, http.StatusBadGateway, w.Code)

	require.Len(t, purger.calls, 1,
		"a rollback is an emergency; a failed audit row must not leave the bad deploy cached at the edge")
}

func TestSiteUndelete_PurgesWhatWasRestoredWhenTheSecondPinFails(t *testing.T) {
	store := newFakeR2()
	store.putAliasFail = map[string]error{"www/preview": errAliasRestore}
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Registry = &reservedRegistry{RegistryWriter: h.Registry, reservation: registry.Reservation{
		PrevProduction: "20260420-141522-abc1234",
		PrevPreview:    "20260421-090000-def5678",
	}}
	h.Reservations = &fakeReservations{}
	h.ReservationGrace = 72 * time.Hour
	h.Audit = &fakeAudit{}
	h.Index = &fakeIndex{}
	purger := &fakePurger{}
	h.EdgePurge = purger

	r := chi.NewRouter()
	r.Post("/api/site/{slug}/undelete", h.SiteUndelete)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/undelete", nil).
		WithContext(contextWithLogin(context.Background(), "alice", "tok"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusOK, w.Code)

	require.Len(t, purger.calls, 1,
		"production is back and serving; the edge still holds the takedown, so the half that succeeded must be purged")
	assert.Equal(t, []string{"www.freecode.camp"}, purger.calls[0],
		"only the pin that actually landed may be purged")
}

var errAliasRestore = errors.New("r2 put outage")

type responseAwarePurger struct {
	calls    [][]string
	sawBody  []bool
	recorder *httptest.ResponseRecorder
}

func (p *responseAwarePurger) PurgeHosts(_ context.Context, hosts []string) error {
	p.calls = append(p.calls, hosts)
	p.sawBody = append(p.sawBody, p.recorder.Body.Len() > 0)
	return nil
}

func TestSitePromote_AnswersTheCallerBeforeWaitingOnCloudflare(t *testing.T) {
	store := newFakeR2()
	store.aliases["www/preview"] = "20260420-141522-abc1234"
	store.objects["www/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.Audit = &fakeAudit{}
	rec := httptest.NewRecorder()
	purger := &responseAwarePurger{recorder: rec}
	h.EdgePurge = purger

	r := chi.NewRouter()
	r.Post("/api/site/{site}/promote", RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.SitePromote))).ServeHTTP)
	req := httptest.NewRequest(http.MethodPost, "/api/site/www/promote", nil)
	for k, v := range bearerTok() {
		req.Header.Set(k, v)
	}
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	require.Len(t, purger.calls, 1)
	assert.True(t, purger.sawBody[0],
		"a purge spends up to a 20s budget on an external API; making the staffer wait for it before the "+
			"response is a stall on the hottest paths, and the error paths in this same file already "+
			"write, flush, then purge")
}
