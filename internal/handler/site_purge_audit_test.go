package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func callPurgeSlug(h *Handlers, slug string) *httptest.ResponseRecorder {
	return withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/"+slug+"?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		contextWithLogin(context.Background(), "alice", "tok"),
	)
}

type failBulkMoveR2 struct {
	*fakeR2
	bulkPrefix string
}

func (f *failBulkMoveR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if src == f.bulkPrefix {
		return 0, errors.New("r2 move outage")
	}
	return f.fakeR2.MovePrefix(ctx, src, dst)
}

type lyingMoveR2 struct {
	*fakeR2
	bulkPrefix string
}

func (l *lyingMoveR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if src == l.bulkPrefix {
		return 7, nil
	}
	return l.fakeR2.MovePrefix(ctx, src, dst)
}

type movePhaseR2 struct {
	*fakeR2
	phases     *[]string
	bulkPrefix string
}

func (m *movePhaseR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if src == m.bulkPrefix {
		*m.phases = append(*m.phases, "bulk")
	} else {
		*m.phases = append(*m.phases, "alias:"+src)
	}
	return m.fakeR2.MovePrefix(ctx, src, dst)
}

func TestSitePurge_AuditsWhenTheRegistryRowIsAlreadyGone(t *testing.T) {
	store := newFakeR2()
	store.objects["orphan/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["orphan/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "orphan")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1,
		"an orphan has no registry row by definition, so every remediation purge would go unaudited")
	assert.Equal(t, "site.purge", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)

	store.mu.Lock()
	defer store.mu.Unlock()
	for k := range store.objects {
		assert.Truef(t, hasPrefix(k, "_trash/orphan/"), "found %q still live after the purge", k)
	}
}

func TestSitePurge_AuditsTheFailureNamingWhatWasAlreadyDestroyed(t *testing.T) {
	store := &failBulkMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	require.Len(t, fa.events, 1,
		"a purge that destroyed the alias and then failed must say so; audit_log is unrebuildable")
	assert.Equal(t, "site.purge", fa.events[0].Action)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "move", fa.events[0].Detail["stage"])
	assert.Equal(t, 1, fa.events[0].Detail["moved"],
		"the production alias was already moved when the bulk move failed")
}

func TestSitePurge_UnpublishesBeforeMovingTheBulkBytes(t *testing.T) {
	var phases []string
	store := &movePhaseR2{fakeR2: newFakeR2(), phases: &phases, bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["example/production"] = []byte("20260420-141522-abc1234")
	store.objects["example/preview"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, phases, 3)
	assert.Equal(t, []string{"alias:example/production", "alias:example/preview", "bulk"}, phases,
		"a site above roughly 215 objects cannot finish its bulk move inside the 10-minute budget; "+
			"moving the two aliases first takes it off the internet in the first second regardless of size")
}

func TestSitePurge_RefusesSuccessWhileObjectsRemain(t *testing.T) {
	store := &lyingMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "r2_move_incomplete")

	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "incomplete", fa.events[0].Detail["stage"])

	listW := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, listW.Code)
	assert.Contains(t, listW.Body.String(), "example",
		"an incomplete purge must not deregister the site: the bytes are still there and the retry needs it")
}

type unlockErrLocker struct{}

func (unlockErrLocker) WithSiteLock(_ context.Context, _ sitekey.Dirname, fn func() error) error {
	if err := fn(); err != nil {
		return err
	}
	return errors.New("site unlock example: conn closed")
}

type verifyErrR2 struct {
	*fakeR2
}

func (v *verifyErrR2) HasPrefix(_ context.Context, _ string) (bool, error) {
	return false, errors.New("r2 list outage")
}

type failAliasMoveR2 struct {
	*fakeR2
	bulkPrefix string
}

func (f *failAliasMoveR2) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	if src != f.bulkPrefix {
		return 0, errors.New("r2 move outage")
	}
	return f.fakeR2.MovePrefix(ctx, src, dst)
}

func TestSitePurge_AuditsSuccessWhenTheUnlockFails(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	h.Locker = unlockErrLocker{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	require.Len(t, fa.events, 1,
		"Handlers.withSiteLock returns the closure's verdict, not the locker's, so a purge that "+
			"fully succeeded is never reported as a failure by a late unlock error")
	assert.Equal(t, "site.purge", fa.events[0].Action)
	assert.Equal(t, "success", fa.events[0].Outcome)
}

func TestSitePurge_AuditsTheUnpublishFailure(t *testing.T) {
	store := &failAliasMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "unpublish", fa.events[0].Detail["stage"])
	assert.Equal(t, 0, fa.events[0].Detail["moved"])
}

func TestSitePurge_AuditsTheVerifyProbeFailure(t *testing.T) {
	store := &verifyErrR2{fakeR2: newFakeR2()}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "r2_verify_failed")

	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "verify", fa.events[0].Detail["stage"],
		"a probe that could not run is not the same as a prefix known to be empty")
}

func TestSitePurge_AuditsTheRegistryFailure(t *testing.T) {
	store := newFakeR2()
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	reg, ok := h.Registry.(*fakeRegistry)
	require.True(t, ok)
	reg.registerErr = errors.New("valkey down")
	fa := &fakeAudit{}
	h.Audit = fa

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	require.Len(t, fa.events, 1)
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "registry", fa.events[0].Detail["stage"],
		"the bytes are already in _trash by this point; a registry outage must not erase that record")
}

func TestSitePurge_AuditsTheFailureAfterTheClientDisconnects(t *testing.T) {
	store := &failBulkMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)
	fa := &fakeAudit{}
	h.Audit = fa

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	withChiRoute(http.MethodDelete, "/api/site/{slug}",
		"/api/site/example?purge=true", nil,
		map[string]string{},
		h.SiteDelete,
		ctx,
	)

	require.Len(t, fa.events, 1,
		"the destructive work runs on a detached ctx, so the record of it must not die with the client")
	assert.Equal(t, "failure", fa.events[0].Outcome)
	assert.Equal(t, "move", fa.events[0].Detail["stage"])
}

func TestSitePurge_FailedBulkMoveKeepsSiteRetryable(t *testing.T) {
	store := &failBulkMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")
	store.objects["example/production"] = []byte("20260420-141522-abc1234")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	registerExample(t, h)

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	store.mu.Lock()
	_, aliasLive := store.objects["example/production"]
	_, bytesLive := store.objects["example/deploys/20260420-141522-abc1234/index.html"]
	store.mu.Unlock()
	assert.False(t, aliasLive, "the alias moved first, so a stalled bulk move still took the site down")
	assert.True(t, bytesLive, "the bulk bytes stay put and the purge is retryable")

	listW := callSitesList(h, "alice", "tok")
	require.Equal(t, http.StatusOK, listW.Code)
	assert.Contains(t, listW.Body.String(), "example",
		"a stalled bulk move must not deregister the site")
}

func TestSitePurge_WritesOneBodyWhenTheUnlockAlsoFails(t *testing.T) {
	store := &failBulkMoveR2{fakeR2: newFakeR2(), bulkPrefix: "example/"}
	store.objects["example/deploys/20260420-141522-abc1234/index.html"] = []byte("hi")

	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), store)
	h.Tombstones = &fakeTombstones{}
	h.Locker = unlockErrLocker{}
	registerExample(t, h)

	w := callPurgeSlug(h, "example")
	require.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())

	var body map[string]any
	dec := json.NewDecoder(strings.NewReader(w.Body.String()))
	require.NoError(t, dec.Decode(&body))
	require.False(t, dec.More(),
		"the closure already answered; appending writeLockError's body would emit two JSON objects")
	assert.Equal(t, "r2_move_failed", body["error"].(map[string]any)["code"])
}
