//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	lifecycleGuardEnv = "ARTEMIS_LIFECYCLE_OK"
	aliasLRUWindow    = 15 * time.Second
	cleanupBudget     = 3 * time.Minute
	aliasReadBudget   = 90 * time.Second
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func lifecycleCfg(t *testing.T, need time.Duration) (cfg, string) {
	t.Helper()
	c := loadCfg(t)
	if os.Getenv(lifecycleGuardEnv) != "1" {
		t.Skipf("%s != 1 — this suite registers, deletes and releases a real slug on the target deployment", lifecycleGuardEnv)
	}
	approver := os.Getenv("GH_APPROVER_TOKEN")
	if approver == "" {
		t.Skip("GH_APPROVER_TOKEN unset — required to release the throwaway slug, without which every run leaks a live site")
	}
	if deadline, ok := t.Deadline(); ok {
		if left := time.Until(deadline); left < need+cleanupBudget {
			t.Skipf("go test -timeout leaves %s, this test needs %s including cleanup; a timeout kill panics the binary and skips t.Cleanup, which would leak the slug — use just integration-lifecycle",
				left.Round(time.Second), (need + cleanupBudget).Round(time.Second))
		}
	}
	return c, approver
}

func lifecycleSlug() string {
	return fmt.Sprintf("it-lc-%d", time.Now().UnixNano())
}

func lifecycleTeam() string { return envDefault("REGISTRY_TEAM", "staff") }

func lifecycleRegisterBody(slug string) map[string]any {
	return map[string]any{"slug": slug, "teams": []string{lifecycleTeam()}}
}

func whoamiLogin(ctx context.Context, t *testing.T, c cfg, token string) string {
	t.Helper()
	var who struct {
		Login string `json:"login"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/whoami", token, nil, &who); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	return who.Login
}

func sameGitHubIdentity(ctx context.Context, t *testing.T, c cfg, approver string) bool {
	t.Helper()
	return whoamiLogin(ctx, t, c, c.GHToken) == whoamiLogin(ctx, t, c, approver)
}

func reclaimLifecycleSlug(t *testing.T, c cfg, approver, slug string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	status, body := c.statusOnly(ctx, http.MethodDelete, "/api/site/"+slug, c.GHToken, nil)
	t.Logf("[cleanup] DELETE %s -> %d", slug, status)
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
		t.Errorf("[cleanup] delete %s: status=%d body=%s — the slug is left behind", slug, status, body)
	}
	status, body = c.statusOnly(ctx, http.MethodPost, "/api/site/"+slug+"/release", approver, nil)
	t.Logf("[cleanup] RELEASE %s -> %d", slug, status)
	switch {
	case status == http.StatusOK:
	case status == http.StatusNotFound && strings.Contains(string(body), "not_found"):
	default:
		t.Errorf("[cleanup] release %s: status=%d body=%s — the slug may stay reserved for the full grace. 403 means the token is not on REPO_APPROVE_AUTHZ_TEAM, 500 misconfigured means that team is unset, 503 means the reservation store is unwired, and a 404 without a not_found code is chi route-not-found on a pre-1.10.0 target", slug, status, body)
	}
}

func registerLifecycleSite(ctx context.Context, t *testing.T, c cfg, slug string) {
	t.Helper()
	var reg struct {
		Slug  string `json:"slug"`
		State string `json:"state"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/site/register", c.GHToken, lifecycleRegisterBody(slug), &reg); err != nil {
		t.Fatalf("register %s: %v", slug, err)
	}
	if reg.Slug != slug {
		t.Fatalf("register echoed slug=%q want %q", reg.Slug, slug)
	}
	if reg.State != "active" {
		t.Fatalf("register %s: state=%q want active", slug, reg.State)
	}
}

func siteState(ctx context.Context, t *testing.T, c cfg, slug string) (string, string) {
	t.Helper()
	var sites []struct {
		Slug          string `json:"slug"`
		State         string `json:"state"`
		ReservedUntil string `json:"reservedUntil"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/sites", c.GHToken, nil, &sites); err != nil {
		t.Fatalf("list sites: %v", err)
	}
	for _, s := range sites {
		if s.Slug == slug {
			return s.State, s.ReservedUntil
		}
	}
	return "", ""
}

func aliasDeployID(ctx context.Context, t *testing.T, c cfg, slug, mode string, budget time.Duration) (string, error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		var alias struct {
			DeployID string `json:"deployId"`
		}
		err := c.doJSON(ctx, http.MethodGet,
			fmt.Sprintf("/api/site/%s/alias/%s", slug, mode), c.GHToken, nil, &alias)
		if err == nil {
			return alias.DeployID, nil
		}
		lastErr = err
		t.Logf("[alias] %s/%s not readable yet: %v", slug, mode, err)
		if werr := sleepCtx(ctx, 5*time.Second); werr != nil {
			return "", fmt.Errorf("%w (last alias error: %v)", werr, lastErr)
		}
	}
	return "", lastErr
}

func TestSiteLifecycle_DeleteHoldsTheNameAndUndeleteRestoresService(t *testing.T) {
	c, approver := lifecycleCfg(t, 20*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	slug := lifecycleSlug()
	siteURL := fmt.Sprintf("https://%s.%s/", slug, c.RootDomain)
	marker := fmt.Sprintf("artemis-lifecycle-%d", time.Now().UnixNano())
	body := []byte(fmt.Sprintf("<!doctype html><html><body><h1>%s</h1></body></html>\n", marker))

	t.Cleanup(func() { reclaimLifecycleSlug(t, c, approver, slug) })

	t.Logf("[1/9] register %s", slug)
	registerLifecycleSite(ctx, t, c, slug)

	t.Logf("[2/9] deploy + promote %s", slug)
	deployID := publishLifecycleSite(ctx, t, c, slug, body)

	t.Logf("[3/9] GET %s — poll %s for the marker", siteURL, c.ProdSLO)
	if err := c.pollForMarker(t, siteURL, marker, c.ProdSLO); err != nil {
		t.Fatalf("site never served before the delete: %v", err)
	}

	t.Logf("[4/9] DELETE /api/site/%s", slug)
	status, respBody := c.statusOnly(ctx, http.MethodDelete, "/api/site/"+slug, c.GHToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s want 204", status, respBody)
	}

	t.Logf("[5/9] GET %s — must go dark", siteURL)
	if err := pollFor404(t, c, siteURL, c.ProdSLO); err != nil {
		t.Fatalf("a deregistered site that keeps serving is the defect this release exists to remove: %v", err)
	}
	darkAt := time.Now()

	t.Logf("[6/9] re-register %s — must be refused", slug)
	status, respBody = c.statusOnly(ctx, http.MethodPost, "/api/site/register", c.GHToken, lifecycleRegisterBody(slug))
	if status != http.StatusConflict {
		t.Fatalf("re-register: status=%d body=%s want 409", status, respBody)
	}
	if !strings.Contains(string(respBody), "site_reserved") {
		t.Fatalf("re-register: body=%s want a site_reserved code", respBody)
	}

	t.Logf("[7/9] GET /api/sites — the hold must be visible on the list")
	state, until := siteState(ctx, t, c, slug)
	if state != "reserved" {
		t.Fatalf("listed %s with state=%q want reserved; universe sites ls shows a held name as live otherwise", slug, state)
	}
	deadline, err := time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("reservedUntil=%q is not RFC3339: %v", until, err)
	}
	if time.Until(deadline) <= 0 {
		t.Fatalf("reservedUntil=%s is already past; the grace must hold the name into the future or undelete has no window", until)
	}

	t.Logf("[8/9] POST /api/site/%s/undelete", slug)
	var undel struct {
		Slug           string `json:"slug"`
		PrevProduction string `json:"prevProduction"`
		PrevPreview    string `json:"prevPreview"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/site/"+slug+"/undelete", c.GHToken, nil, &undel); err != nil {
		t.Fatalf("undelete %s: %v", slug, err)
	}
	if undel.PrevProduction != deployID {
		t.Fatalf("undelete: prevProduction=%q want %q; the pointers are returned exactly once and the server clears them in the same statement",
			undel.PrevProduction, deployID)
	}
	if undel.PrevPreview != deployID {
		t.Fatalf("undelete: prevPreview=%q want %q; promote leaves preview pinned at the finalized deploy", undel.PrevPreview, deployID)
	}
	if state, _ := siteState(ctx, t, c, slug); state != "active" {
		t.Fatalf("after undelete %s is state=%q want active; restoring the pins without clearing the hold leaves the sweep free to reclaim it", slug, state)
	}

	for _, mode := range []string{"production", "preview"} {
		got, err := aliasDeployID(ctx, t, c, slug, mode, aliasReadBudget)
		if err != nil {
			t.Fatalf("alias %s read after undelete: %v", mode, err)
		}
		if got != deployID {
			t.Fatalf("alias %s after undelete: deployId=%q want %q; without the R2 pin gc-site collects the deploy undelete rescued",
				mode, got, deployID)
		}
	}

	if wait := aliasLRUWindow - time.Since(darkAt); wait > 0 {
		t.Logf("[9/9] waiting %s past the last 404 so a stale 15s alias LRU entry cannot answer", wait.Round(time.Second))
		if err := sleepCtx(ctx, wait); err != nil {
			t.Fatalf("waiting out the alias LRU: %v", err)
		}
	}
	bust := fmt.Sprintf("%s?cb=%d", siteURL, time.Now().UnixNano())
	t.Logf("[9/9] GET %s — must serve again with no redeploy", bust)
	if err := c.pollForMarker(t, bust, marker, c.ProdSLO); err != nil {
		t.Fatalf("undelete restored the row but the site stays dark: %v", err)
	}
	t.Logf("OK — delete held %s, undelete returned it to service pinned at %s", slug, deployID)
}

func TestSiteLifecycle_ReleaseRefusesAStaffToken(t *testing.T) {
	c, approver := lifecycleCfg(t, 2*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if sameGitHubIdentity(ctx, t, c, approver) {
		t.Skipf("GH_TOKEN and GH_APPROVER_TOKEN resolve to the same GitHub login — the gate authorizes on the account's team membership, not the token string, so this identity cannot be refused and the call would release the site instead. Supply a staff token that is NOT on REPO_APPROVE_AUTHZ_TEAM to cover this.")
	}

	slug := lifecycleSlug() + "n"
	status, respBody := c.statusOnly(ctx, http.MethodPost, "/api/site/"+slug+"/release", c.GHToken, nil)
	if status != http.StatusForbidden {
		t.Fatalf("staff release of an unregistered slug: status=%d body=%s want 403; requireRepoTeam runs before the slug regex and before any state read, so a staff caller must be refused whether or not the name exists",
			status, respBody)
	}
	t.Logf("OK — a staff token cannot reach release at all")
}

func TestSiteLifecycle_ReleaseFreesTheNameAndTheBytes(t *testing.T) {
	c, approver := lifecycleCfg(t, 10*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	slug := lifecycleSlug() + "r"
	marker := fmt.Sprintf("artemis-release-%d", time.Now().UnixNano())
	body := []byte(fmt.Sprintf("<!doctype html><html><body><h1>%s</h1></body></html>\n", marker))

	t.Cleanup(func() { reclaimLifecycleSlug(t, c, approver, slug) })

	registerLifecycleSite(ctx, t, c, slug)
	publishLifecycleSite(ctx, t, c, slug, body)

	status, respBody := c.statusOnly(ctx, http.MethodDelete, "/api/site/"+slug, c.GHToken, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status=%d body=%s want 204", status, respBody)
	}

	t.Logf("release %s with the approver token", slug)
	var rel struct {
		Slug   string `json:"slug"`
		Status string `json:"status"`
		Moved  int    `json:"moved"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/site/"+slug+"/release", approver, nil, &rel); err != nil {
		t.Fatalf("approver release %s: %v", slug, err)
	}
	if rel.Status != "released" {
		t.Fatalf("release: status=%q want released", rel.Status)
	}
	if rel.Moved == 0 {
		t.Fatalf("release moved 0 objects for a site that published one file; bytes left at the origin prefix are inherited by the next owner of the name")
	}

	status, respBody = c.statusOnly(ctx, http.MethodPost, "/api/site/register", c.GHToken, lifecycleRegisterBody(slug))
	if status != http.StatusCreated {
		t.Fatalf("re-register after release: status=%d body=%s want 201, the name must be free", status, respBody)
	}

	left, err := deployCountAfterRelease(ctx, t, c, slug, aliasReadBudget)
	if err != nil {
		t.Fatalf("deploys after release: %v", err)
	}
	if left != 0 {
		t.Fatalf("release left %d deploys on %s; SiteRelease runs no post-move verify, so a partial MovePrefix reports moved>0 while the next owner of the name inherits the bytes",
			left, slug)
	}
	t.Logf("OK — release freed %s after moving %d objects, and the name came back empty", slug, rel.Moved)
}

func publishLifecycleSite(ctx context.Context, t *testing.T, c cfg, slug string, body []byte) string {
	t.Helper()
	sha := fmt.Sprintf("lc%07d", time.Now().UnixNano()%10_000_000)
	initReq := map[string]any{"site": slug, "sha": sha, "files": []string{"index.html"}}
	var initResp struct {
		DeployID string `json:"deployId"`
		JWT      string `json:"jwt"`
	}
	deadline := time.Now().Add(90 * time.Second)
	var initErr error
	for time.Now().Before(deadline) {
		initErr = c.doJSON(ctx, http.MethodPost, "/api/deploy/init", c.GHToken, initReq, &initResp)
		if initErr == nil {
			break
		}
		t.Logf("[init] %s not authorizable yet, the registry snapshot refreshes on pub-sub with a 60s TTL fallback: %v", slug, initErr)
		if werr := sleepCtx(ctx, 5*time.Second); werr != nil {
			t.Fatalf("init %s: %v (last init error: %v)", slug, werr, initErr)
		}
	}
	if initErr != nil {
		t.Fatalf("init %s: %v", slug, initErr)
	}
	if err := c.doUpload(ctx, initResp.DeployID, initResp.JWT,
		"index.html", "text/html; charset=utf-8", body, nil); err != nil {
		t.Fatalf("upload %s: %v", slug, err)
	}
	finReq := map[string]any{"mode": "preview", "files": []string{"index.html"}}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/deploy/%s/finalize", initResp.DeployID), initResp.JWT, finReq, nil); err != nil {
		t.Fatalf("finalize %s: %v", slug, err)
	}
	var promote struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", slug), c.GHToken,
		map[string]any{"deployId": initResp.DeployID}, &promote); err != nil {
		t.Fatalf("promote %s: %v", slug, err)
	}
	if promote.DeployID != initResp.DeployID {
		t.Fatalf("promote %s: deployId=%q want %q", slug, promote.DeployID, initResp.DeployID)
	}
	return initResp.DeployID
}

func deployCountAfterRelease(ctx context.Context, t *testing.T, c cfg, slug string, budget time.Duration) (int, error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		var deploys []struct {
			DeployID string `json:"deployId"`
		}
		err := c.doJSON(ctx, http.MethodGet, "/api/site/"+slug+"/deploys", c.GHToken, nil, &deploys)
		if err == nil {
			return len(deploys), nil
		}
		lastErr = err
		t.Logf("[deploys] %s not readable yet, the re-registered row reaches the snapshot on pub-sub with a 60s TTL fallback: %v", slug, err)
		if werr := sleepCtx(ctx, 5*time.Second); werr != nil {
			return 0, fmt.Errorf("%w (last deploys error: %v)", werr, lastErr)
		}
	}
	return 0, lastErr
}
