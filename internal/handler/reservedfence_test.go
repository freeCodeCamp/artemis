package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Production drops a reserved site from the cached snapshot
// (internal/registry/valkey/reader.go, Refresh skips IsReserved), so the
// team lookup that authorises a deploy cannot tell a held name from one
// that never existed. Without a registry read the caller is told the
// site is not registered, which sends them to register it — and that is
// refused too. The fakes keep the two stores independent, so a test has
// to drop the site from the snapshot to reproduce what production does.
// reserveSite models what a delete actually does: the registry row
// flips to reserved AND the cached snapshot drops the site, because
// valkey.Reader.Refresh skips IsReserved. Reserving in the registry
// alone leaves the fakes in a state no production path can produce, and
// a fence test written against it proves less than it looks.
func reserveSite(h *Handlers, reg *fakeRegistry, slug sitekey.Slug, until time.Time) {
	reg.reserve(slug, until)
	delete(h.Sites.(*fakeSites).bySite, slug)
}

func TestDeployInit_ReservedSiteAnswers409NotSiteUnauthorized(t *testing.T) {
	h, _, reg := fencedHandlers(t, newFakeR2())
	reserveSite(h, reg, "www", fenceDeadline)

	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init",
		strings.NewReader(`{"site":"www","sha":"abcdef1234"}`))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.DeployInit))).ServeHTTP(w, r)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	errObj := fenceErrorBody(t, w)
	assert.Equal(t, "site_reserved", errObj["code"],
		"a held name must not read as unregistered; the caller's next move depends on the difference")
	assert.Equal(t, fenceDeadline.Format(time.RFC3339), errObj["reservedUntil"],
		"the caller cannot wait out a hold whose deadline they are never told")
}

// A slug the registry has never heard of must still be refused the same
// way it always was, or every typo becomes a registry read.
func TestDeployInit_UnknownSiteStillAnswers403(t *testing.T) {
	h, _, _ := fencedHandlers(t, newFakeR2())

	r := httptest.NewRequest(http.MethodPost, "/api/deploy/init",
		strings.NewReader(`{"site":"never-registered","sha":"abcdef1234"}`))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	RequestID(h.RequireGitHubBearer(http.HandlerFunc(h.DeployInit))).ServeHTTP(w, r)

	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, "site_unauthorized", fenceErrorBody(t, w)["code"])
}

// SiteDeployDelete authorises through requireSiteAuthz, which reads the
// same snapshot, so a reserved site reaches it as an unknown one. It is
// the last site or deploy mutator without its own requireWritableSite
// call, and this pins that the shared authz path fences it anyway.
func TestSiteDeployDelete_ReservedSiteAnswers409(t *testing.T) {
	deployID := "20260420-141522-abc1234"
	store := newFakeR2()
	store.objects["www/deploys/"+deployID+"/index.html"] = []byte("hi")
	h, _, reg := fencedHandlers(t, store)
	h.Tombstones = &fakeTombstones{}
	reserveSite(h, reg, "www", fenceDeadline)

	w := withSiteRoute(http.MethodDelete, "/api/site/{site}/deploys/{deployId}",
		"/api/site/www/deploys/"+deployID, nil,
		contextWithLogin(context.Background(), "alice", "tok"), h.SiteDeployDelete)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "site_reserved", fenceErrorBody(t, w)["code"])
	store.mu.Lock()
	defer store.mu.Unlock()
	_, inTrash := store.objects["_trash/www/"+deployID+"/index.html"]
	assert.False(t, inTrash, "a held site's bytes must not move while the name is reserved")
}

// The deploy-session JWT is a stateless signature and expiry check, so a
// token minted while the site was live stays valid for its whole TTL.
// If the site is deleted mid-deploy, RequireDeployJWT is the first thing
// the upload and finalize calls hit, and it reads the same snapshot.
// Without the fence there it answers 403 and short-circuits the
// registry-backed check inside DeployFinalize, so the correct 409 is
// unreachable for the one race every deploy has.
func TestDeployUpload_ReservedMidDeployAnswers409(t *testing.T) {
	h, jwt, reg := fencedHandlers(t, newFakeR2())
	deployID := "20260420-141522-abc1234"
	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)

	reserveSite(h, reg, "www", fenceDeadline)

	w := withChiRoute(http.MethodPut, "/api/deploy/{deployId}/upload",
		"/api/deploy/"+deployID+"/upload?path=index.html",
		[]byte("<h1>hi</h1>"),
		map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "text/html"},
		RequestID(h.RequireDeployJWT(http.HandlerFunc(h.DeployUpload))).ServeHTTP,
		context.Background(),
	)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	errObj := fenceErrorBody(t, w)
	assert.Equal(t, "site_reserved", errObj["code"])
	assert.Equal(t, fenceDeadline.Format(time.RFC3339), errObj["reservedUntil"])
}
