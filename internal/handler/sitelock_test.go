package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (e *eventLog) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, s)
}

type fakeLocker struct {
	log *eventLog
}

func (l *fakeLocker) WithSiteLock(_ context.Context, site string, fn func() error) error {
	l.log.add("lock:" + site)
	err := fn()
	l.log.add("unlock:" + site)
	return err
}

type loggingR2 struct {
	*fakeR2
	log *eventLog
}

func (r *loggingR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	r.log.add("move:" + src)
	return r.fakeR2.MovePrefix(ctx, src, dst)
}

func (r *loggingR2) PutAlias(ctx context.Context, key, deployID string) error {
	r.log.add("putAlias:" + key)
	return r.fakeR2.PutAlias(ctx, key, deployID)
}

func assertInsideLock(t *testing.T, log *eventLog, lockKey, op string) {
	t.Helper()
	lockAt, opAt, unlockAt := -1, -1, -1
	for i, e := range log.events {
		switch e {
		case "lock:" + lockKey:
			lockAt = i
		case op:
			opAt = i
		case "unlock:" + lockKey:
			unlockAt = i
		}
	}
	require.GreaterOrEqual(t, lockAt, 0, "site lock %q acquired; events=%v", lockKey, log.events)
	require.GreaterOrEqual(t, opAt, 0, "op %q happened; events=%v", op, log.events)
	require.GreaterOrEqual(t, unlockAt, 0, "site lock released; events=%v", log.events)
	assert.True(t, lockAt < opAt && opAt < unlockAt,
		"destructive op must run inside the per-site lock; events=%v", log.events)
}

func TestSiteDeployDelete_SerializedUnderSiteLock(t *testing.T) {
	deployID := "20260101-000000-old0001"
	log := &eventLog{}
	store := &loggingR2{fakeR2: newFakeR2(), log: log}
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("old")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Tombstones = &fakeTombstones{}
	h.Locker = &fakeLocker{log: log}

	w := callDeployDelete(h, "www", deployID)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www.freecode.camp", "move:www.freecode.camp/deploys/"+deployID+"/")
}

func TestSitePurge_SerializedUnderSiteLock(t *testing.T) {
	log := &eventLog{}
	store := &loggingR2{fakeR2: newFakeR2(), log: log}
	store.objects["example.freecode.camp/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Tombstones = &fakeTombstones{}
	h.Locker = &fakeLocker{log: log}

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	w := withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "example.freecode.camp", "move:example.freecode.camp/")
}

func TestSitePromote_AliasWriteUnderSiteLock(t *testing.T) {
	log := &eventLog{}
	store := &loggingR2{fakeR2: newFakeR2(), log: log}

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeLocker{log: log}

	deployID := "20260420-141522-abc1234"
	body, _ := json.Marshal(SitePromoteRequest{DeployID: deployID})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SitePromote,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www.freecode.camp", "putAlias:www/production")
}

func TestSiteRollback_AliasWriteUnderSiteLock(t *testing.T) {
	log := &eventLog{}
	store := &loggingR2{fakeR2: newFakeR2(), log: log}
	deployID := "20260101-000000-old0001"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("old")

	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeLocker{log: log}

	body, _ := json.Marshal(SiteRollbackRequest{To: deployID})
	w := withSiteRoute(http.MethodPost, "/api/site/{site}/rollback",
		"/api/site/www/rollback", body,
		contextWithLogin(context.Background(), "alice", "tok"),
		h.SiteRollback,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www.freecode.camp", "putAlias:www/production")
}

func TestDeployFinalize_AliasWriteUnderSiteLock(t *testing.T) {
	log := &eventLog{}
	store := &loggingR2{fakeR2: newFakeR2(), log: log}

	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	h.Locker = &fakeLocker{log: log}

	deployID := "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("hi")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})

	w := withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		context.Background(),
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assertInsideLock(t, log, "www.freecode.camp", "putAlias:www/preview")
}
