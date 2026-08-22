package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type cancelOnPutAliasR2 struct {
	*fakeR2
	cancel context.CancelFunc
}

func (c *cancelOnPutAliasR2) PutAlias(ctx context.Context, key, deployID string) error {
	c.cancel()
	return c.fakeR2.PutAlias(ctx, key, deployID)
}

type ctxAwareIndex struct {
	mu        sync.Mutex
	finalized []string
	aliased   []string
}

func (f *ctxAwareIndex) FinalizeAtomic(ctx context.Context, site sitekey.Dirname, deployID, mode string, _ time.Time, _ int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalized = append(f.finalized, string(site)+"/"+deployID+"/"+mode)
	return nil
}

func (f *ctxAwareIndex) AliasAtomic(ctx context.Context, site sitekey.Dirname, name, deployID string, _ time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aliased = append(f.aliased, string(site)+"/"+name+"/"+deployID)
	return nil
}

func TestDeployFinalize_ClientAbort_StillWritesIndex(t *testing.T) {
	store := newFakeR2()
	reqCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, jwt := newTestHandlers(t, &fakeGH{}, standardSites(), store)
	h.R2 = &cancelOnPutAliasR2{fakeR2: store, cancel: cancel}
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	idx := &ctxAwareIndex{}
	h.Index = idx

	deployID := "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("hi")

	tok, _, err := jwt.Sign("alice", "www", deployID)
	require.NoError(t, err)
	body, _ := json.Marshal(DeployFinalizeRequest{Mode: "preview", Files: []string{"index.html"}})

	withChiRoute(http.MethodPost, "/api/deploy/{deployId}/finalize",
		"/api/deploy/"+deployID+"/finalize", body,
		map[string]string{"Authorization": "Bearer " + tok},
		h.RequireDeployJWT(http.HandlerFunc(h.DeployFinalize)).ServeHTTP,
		reqCtx,
	)

	require.Len(t, idx.finalized, 1,
		"alias landed in R2, so the index write must survive the client abort — else R2 and Postgres diverge")
}

func TestSitePromote_ClientAbort_StillWritesIndex(t *testing.T) {
	store := newFakeR2()
	reqCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, _ := newTestHandlers(t, authedGH(), standardSites(), store)
	h.R2 = &cancelOnPutAliasR2{fakeR2: store, cancel: cancel}
	h.DeployPrefix = mustDeployPrefixTemplate(prodShapedFormat)
	idx := &ctxAwareIndex{}
	h.Index = idx

	deployID := "20260420-141522-abc1234"
	store.objects["www.freecode.camp/deploys/"+deployID+"/index.html"] = []byte("hi")

	body, _ := json.Marshal(SitePromoteRequest{DeployID: deployID})
	withSiteRoute(http.MethodPost, "/api/site/{site}/promote",
		"/api/site/www/promote", body,
		contextWithLogin(reqCtx, "alice", "tok"),
		h.SitePromote,
	)

	require.Len(t, idx.aliased, 1,
		"alias landed in R2, so the alias row must survive the client abort")
}

type ctxAwareRegistry struct {
	*fakeRegistry
}

func (c *ctxAwareRegistry) GetSite(ctx context.Context, slug sitekey.Slug) (registry.Site, error) {
	if err := ctx.Err(); err != nil {
		return registry.Site{}, err
	}
	return c.fakeRegistry.GetSite(ctx, slug)
}

func (c *ctxAwareRegistry) UpdateTeams(ctx context.Context, slug sitekey.Slug, teams []string) (registry.Site, error) {
	if err := ctx.Err(); err != nil {
		return registry.Site{}, err
	}
	return c.fakeRegistry.UpdateTeams(ctx, slug, teams)
}

func TestSiteUpdate_ClientAbort_StillWritesTeams(t *testing.T) {
	h, _ := newTestHandlers(t, staffCallerGH(), standardSites(), newFakeR2())
	reg := &ctxAwareRegistry{fakeRegistry: newFakeRegistry()}
	h.Registry = reg

	regBody, _ := json.Marshal(SiteRegisterRequest{Slug: "example", Teams: []string{"staff"}})
	require.Equal(t, http.StatusCreated, callRegister(h, regBody, "alice", "tok").Code)

	ctx, cancel := context.WithCancel(contextWithLogin(context.Background(), "alice", "tok"))
	cancel()

	updBody, _ := json.Marshal(SiteUpdateRequest{Teams: []string{"news-editors"}})
	w := withChiRoute(http.MethodPatch, "/api/site/{slug}",
		"/api/site/example", updBody,
		map[string]string{},
		h.SiteUpdate,
		ctx,
	)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "news-editors",
		"an empty body also records 200 on a ResponseRecorder, so the body must carry the new row")

	after, err := reg.GetSite(context.Background(), sitekey.Slug("example"))
	require.NoError(t, err)
	require.Equal(t, []string{"news-editors"}, after.Teams,
		"949bbd8 detached every other commit path so a client hangup cannot cancel a half-finished "+
			"mutation with the advisory lock held; SiteUpdate was the one it missed")
}
