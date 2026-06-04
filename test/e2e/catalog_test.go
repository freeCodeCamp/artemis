//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHealthZ(t *testing.T) {
	e := requireStack(t)
	var resp struct {
		OK bool `json:"ok"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/healthz", "", nil, &resp), http.StatusOK, "healthz")
	if !resp.OK {
		t.Fatalf("healthz ok=false")
	}
}

func TestReadyZ_FullyHealthy(t *testing.T) {
	e := requireStack(t)
	var resp struct {
		Ready    bool `json:"ready"`
		Degraded bool `json:"degraded"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/readyz", "", nil, &resp), http.StatusOK, "readyz")
	if !resp.Ready {
		t.Fatalf("readyz ready=false")
	}
	if resp.Degraded {
		t.Fatalf("readyz degraded=true with PG up; want non-degraded (valkey+r2+pg all reachable)")
	}
}

func TestMetrics(t *testing.T) {
	e := requireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, body, err := e.raw(ctx, http.MethodGet, "/metrics", "", nil)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	mustStatus(t, status, http.StatusOK, "metrics")
	if !strings.Contains(string(body), "artemis_") && !strings.Contains(string(body), "go_") {
		t.Fatalf("metrics body missing prometheus exposition: %s", truncate(body, 200))
	}
}

func TestWhoAmI(t *testing.T) {
	e := requireStack(t)
	var resp struct {
		Login           string   `json:"login"`
		AuthorizedSites []string `json:"authorizedSites"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/whoami", e.GHToken, nil, &resp), http.StatusOK, "whoami")
	if resp.Login != "smoke-bot" {
		t.Fatalf("whoami login=%q want smoke-bot", resp.Login)
	}
}

func TestAuthRejections(t *testing.T) {
	e := requireStack(t)

	t.Run("missing_token", func(t *testing.T) {
		mustStatus(t, e.call(t, http.MethodGet, "/api/whoami", "", nil, nil), http.StatusUnauthorized, "missing token")
	})

	t.Run("missing_deploy_jwt", func(t *testing.T) {
		mustStatus(t, e.call(t, http.MethodPost, "/api/deploy/20260101-000000-abc1234/finalize", "", nil, nil),
			http.StatusUnauthorized, "missing deploy jwt")
	})

	t.Run("unknown_site", func(t *testing.T) {
		body := map[string]any{"site": "neverregistered", "sha": "deadbeef"}
		mustStatus(t, e.call(t, http.MethodPost, "/api/deploy/init", e.GHToken, body, nil),
			http.StatusForbidden, "unknown site")
	})

	t.Run("missing_site_field", func(t *testing.T) {
		body := map[string]any{"sha": "abc"}
		mustStatus(t, e.call(t, http.MethodPost, "/api/deploy/init", e.GHToken, body, nil),
			http.StatusBadRequest, "missing site")
	})
}

func TestRegistryCRUD(t *testing.T) {
	e := requireStack(t)
	slug := uniqueSlug("reg")

	var created struct {
		Slug  string   `json:"slug"`
		Teams []string `json:"teams"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/register", e.GHToken,
		map[string]any{"slug": slug, "teams": []string{"staff"}}, &created), http.StatusCreated, "register")
	if created.Slug != slug {
		t.Fatalf("register slug=%q want %q", created.Slug, slug)
	}

	t.Run("duplicate_conflict", func(t *testing.T) {
		mustStatus(t, e.call(t, http.MethodPost, "/api/site/register", e.GHToken,
			map[string]any{"slug": slug, "teams": []string{"staff"}}, nil), http.StatusConflict, "register dup")
	})

	var list []struct {
		Slug string `json:"slug"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/sites", e.GHToken, nil, &list), http.StatusOK, "sites list")
	if !containsSlug(list, slug) {
		t.Fatalf("sites list missing %q", slug)
	}

	var patched struct {
		Teams []string `json:"teams"`
	}
	mustStatus(t, e.call(t, http.MethodPatch, "/api/site/"+slug, e.GHToken,
		map[string]any{"teams": []string{"staff", "apollo-11-approvers"}}, &patched), http.StatusOK, "patch")
	if len(patched.Teams) != 2 {
		t.Fatalf("patch teams=%v want 2", patched.Teams)
	}

	mustStatus(t, e.call(t, http.MethodGet, "/api/site/"+slug+"/alias/production", e.GHToken, nil, nil),
		http.StatusNotFound, "alias before finalize")

	mustStatus(t, e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil), http.StatusNoContent, "delete")

	t.Run("delete_missing", func(t *testing.T) {
		mustStatus(t, e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil),
			http.StatusNotFound, "delete missing")
	})
}

func TestDeployFlow_DeepAssert(t *testing.T) {
	e := requireStack(t)
	r2c := e.r2Client(t)
	pool := e.pgPool(t)
	ctx := context.Background()

	slug := uniqueSlug("dep")
	registerSite(t, e, slug)
	t.Cleanup(func() { _ = e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil) })

	var initResp struct {
		DeployID string `json:"deployId"`
		JWT      string `json:"jwt"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/deploy/init", e.GHToken,
		map[string]any{"site": slug, "sha": deploySHA(), "files": []string{"index.html"}}, &initResp),
		http.StatusOK, "init")
	if initResp.DeployID == "" || initResp.JWT == "" {
		t.Fatalf("init empty deployId/jwt: %+v", initResp)
	}

	html := []byte("<!doctype html><html><body>e2e</body></html>\n")
	var upResp struct {
		Received string `json:"received"`
		Key      string `json:"key"`
	}
	mustStatus(t, e.upload(t, initResp.DeployID, initResp.JWT, "index.html", "text/html", html, &upResp),
		http.StatusOK, "upload")
	if upResp.Received != "index.html" {
		t.Fatalf("upload received=%q", upResp.Received)
	}

	if !hasPrefix(t, r2c, upResp.Key) {
		t.Fatalf("R2 object %q absent after upload", upResp.Key)
	}

	var finResp struct {
		DeployID string `json:"deployId"`
		Mode     string `json:"mode"`
	}
	mustStatus(t, e.call(t, http.MethodPost, fmt.Sprintf("/api/deploy/%s/finalize", initResp.DeployID),
		initResp.JWT, map[string]any{"mode": "preview", "files": []string{"index.html"}}, &finResp),
		http.StatusOK, "finalize")
	if finResp.Mode != "preview" {
		t.Fatalf("finalize mode=%q", finResp.Mode)
	}

	previewAlias, err := r2c.GetAlias(ctx, slug+"/preview")
	if err != nil {
		t.Fatalf("R2 preview alias get: %v", err)
	}
	if strings.TrimSpace(previewAlias) != initResp.DeployID {
		t.Fatalf("R2 preview alias=%q want %q", previewAlias, initResp.DeployID)
	}

	waitOutbox(t, pool, slug)

	var promoteResp struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/"+slug+"/promote", e.GHToken,
		map[string]any{"deployId": initResp.DeployID}, &promoteResp), http.StatusOK, "promote")
	if promoteResp.DeployID != initResp.DeployID {
		t.Fatalf("promote deployId=%q want %q", promoteResp.DeployID, initResp.DeployID)
	}

	prodAlias, err := r2c.GetAlias(ctx, slug+"/production")
	if err != nil {
		t.Fatalf("R2 prod alias get: %v", err)
	}
	if strings.TrimSpace(prodAlias) != initResp.DeployID {
		t.Fatalf("R2 prod alias=%q want %q", prodAlias, initResp.DeployID)
	}

	var aliasResp struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/site/"+slug+"/alias/production", e.GHToken, nil, &aliasResp),
		http.StatusOK, "alias get")
	if aliasResp.DeployID != initResp.DeployID {
		t.Fatalf("alias get deployId=%q want %q", aliasResp.DeployID, initResp.DeployID)
	}

	var deploys []struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/site/"+slug+"/deploys", e.GHToken, nil, &deploys),
		http.StatusOK, "deploys list")
	if len(deploys) == 0 || deploys[0].DeployID != initResp.DeployID {
		t.Fatalf("deploys list missing %q: %+v", initResp.DeployID, deploys)
	}
}

func TestSiteRollback_DeepAssert(t *testing.T) {
	e := requireStack(t)
	r2c := e.r2Client(t)
	ctx := context.Background()

	slug := uniqueSlug("rbk")
	registerSite(t, e, slug)
	t.Cleanup(func() { _ = e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil) })

	prior := mintDeploy(t, e, slug, "preview")
	current := mintDeploy(t, e, slug, "preview")
	if prior == current {
		t.Fatalf("deploy ids collided: %q", prior)
	}

	var promoteResp struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/"+slug+"/promote", e.GHToken,
		map[string]any{"deployId": current}, &promoteResp), http.StatusOK, "promote")
	if promoteResp.DeployID != current {
		t.Fatalf("promote deployId=%q want %q", promoteResp.DeployID, current)
	}

	prodAlias, err := r2c.GetAlias(ctx, slug+"/production")
	if err != nil {
		t.Fatalf("R2 prod alias get after promote: %v", err)
	}
	if strings.TrimSpace(prodAlias) != current {
		t.Fatalf("R2 prod alias=%q want %q after promote", prodAlias, current)
	}

	var rollbackResp struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/"+slug+"/rollback", e.GHToken,
		map[string]any{"to": prior}, &rollbackResp), http.StatusOK, "rollback")
	if rollbackResp.DeployID != prior {
		t.Fatalf("rollback deployId=%q want %q", rollbackResp.DeployID, prior)
	}

	rolledAlias, err := r2c.GetAlias(ctx, slug+"/production")
	if err != nil {
		t.Fatalf("R2 prod alias get after rollback: %v", err)
	}
	if strings.TrimSpace(rolledAlias) != prior {
		t.Fatalf("R2 prod alias=%q want %q after rollback", rolledAlias, prior)
	}

	var aliasResp struct {
		DeployID string `json:"deployId"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/site/"+slug+"/alias/production", e.GHToken, nil, &aliasResp),
		http.StatusOK, "alias get")
	if aliasResp.DeployID != prior {
		t.Fatalf("alias get deployId=%q want %q after rollback", aliasResp.DeployID, prior)
	}
}

func TestManualDelete_Tombstone(t *testing.T) {
	e := requireStack(t)
	r2c := e.r2Client(t)
	pool := e.pgPool(t)
	ctx := context.Background()

	slug := uniqueSlug("del")
	registerSite(t, e, slug)
	t.Cleanup(func() { _ = e.call(t, http.MethodDelete, "/api/site/"+slug, e.GHToken, nil, nil) })

	deployID := mintDeploy(t, e, slug, "preview")

	deployPrefix := slug + "/deploys/" + deployID + "/"
	if !hasPrefix(t, r2c, deployPrefix) {
		t.Fatalf("R2 prefix %q absent before delete", deployPrefix)
	}

	t.Run("aliased_conflict", func(t *testing.T) {
		mustStatus(t, e.call(t, http.MethodDelete,
			fmt.Sprintf("/api/site/%s/deploys/%s", slug, deployID), e.GHToken, nil, nil),
			http.StatusConflict, "delete aliased")
	})

	mintDeploy(t, e, slug, "preview")

	var delResp struct {
		Status string `json:"status"`
	}
	mustStatus(t, e.call(t, http.MethodDelete,
		fmt.Sprintf("/api/site/%s/deploys/%s", slug, deployID), e.GHToken, nil, &delResp),
		http.StatusOK, "delete deploy")
	if delResp.Status != "tombstoned" {
		t.Fatalf("delete status=%q want tombstoned", delResp.Status)
	}

	if hasPrefix(t, r2c, deployPrefix) {
		t.Fatalf("R2 prefix %q still present after tombstone", deployPrefix)
	}
	if !hasPrefix(t, r2c, "_trash/"+slug+"/"+deployID+"/") {
		t.Fatalf("R2 trash prefix absent after tombstone")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tombstones WHERE site=$1 AND id=$2`, slug, deployID).Scan(&n); err != nil {
		t.Fatalf("pg tombstone query: %v", err)
	}
	if n != 1 {
		t.Fatalf("pg tombstone rows=%d want 1", n)
	}
}

func TestSitePurge_Tombstone(t *testing.T) {
	e := requireStack(t)
	r2c := e.r2Client(t)
	pool := e.pgPool(t)
	ctx := context.Background()

	slug := uniqueSlug("pur")
	registerSite(t, e, slug)
	mintDeploy(t, e, slug, "preview")

	var purgeResp struct {
		Status string `json:"status"`
	}
	mustStatus(t, e.call(t, http.MethodDelete, "/api/site/"+slug+"?purge=true", e.GHToken, nil, &purgeResp),
		http.StatusOK, "purge")
	if purgeResp.Status != "purged" {
		t.Fatalf("purge status=%q want purged", purgeResp.Status)
	}

	if hasPrefix(t, r2c, slug+"/") {
		t.Fatalf("R2 site prefix %q/ still present after purge", slug)
	}
	if !hasPrefix(t, r2c, "_trash/"+slug+"/") {
		t.Fatalf("R2 trash prefix absent after purge")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM tombstones WHERE site=$1`, slug).Scan(&n); err != nil {
		t.Fatalf("pg tombstone query: %v", err)
	}
	if n == 0 {
		t.Fatalf("pg tombstone rows=0 after purge; want >=1")
	}
}

func TestRepoQueue(t *testing.T) {
	e := requireStack(t)
	name := uniqueSlug("repo")

	var tmpl struct {
		Templates []string `json:"templates"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/repo/templates", e.GHToken, nil, &tmpl), http.StatusOK, "templates")
	if !containsString(tmpl.Templates, "universe-static-template") {
		t.Fatalf("templates missing universe-static-template: %v", tmpl.Templates)
	}

	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/repo", e.GHToken,
		map[string]any{"name": name, "visibility": "public"}, &created), http.StatusCreated, "repo create")
	if created.Status != "pending" || created.ID == "" {
		t.Fatalf("repo create status=%q id=%q", created.Status, created.ID)
	}

	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/repo/"+created.ID, e.GHToken, nil, &got), http.StatusOK, "repo get")
	if got.ID != created.ID {
		t.Fatalf("repo get id=%q want %q", got.ID, created.ID)
	}

	var pending []struct {
		ID string `json:"id"`
	}
	mustStatus(t, e.call(t, http.MethodGet, "/api/repos?status=pending", e.GHToken, nil, &pending),
		http.StatusOK, "repos list")
	if !containsID(pending, created.ID) {
		t.Fatalf("repos pending list missing %q", created.ID)
	}

	var approve struct {
		Outcome string `json:"outcome"`
		Request struct {
			Status string `json:"status"`
		} `json:"request"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/repo/"+created.ID+"/approve", e.GHToken, nil, &approve),
		http.StatusOK, "repo approve")
	if approve.Outcome != "ok" || approve.Request.Status != "active" {
		t.Fatalf("repo approve outcome=%q status=%q", approve.Outcome, approve.Request.Status)
	}

	t.Run("reject_path", func(t *testing.T) {
		other := uniqueSlug("repj")
		var c struct {
			ID string `json:"id"`
		}
		mustStatus(t, e.call(t, http.MethodPost, "/api/repo", e.GHToken,
			map[string]any{"name": other, "visibility": "public"}, &c), http.StatusCreated, "repo create2")
		var rej struct {
			Status string `json:"status"`
		}
		mustStatus(t, e.call(t, http.MethodPost, "/api/repo/"+c.ID+"/reject", e.GHToken,
			map[string]any{"reason": "e2e"}, &rej), http.StatusOK, "repo reject")
		if rej.Status != "rejected" {
			t.Fatalf("repo reject status=%q", rej.Status)
		}
		mustStatus(t, e.call(t, http.MethodDelete, "/api/repo/"+c.ID, e.GHToken, nil, nil),
			http.StatusNoContent, "repo delete")
	})
}
