//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/r2"
)

var slugSeq atomic.Int64

func uniqueSlug(prefix string) string {
	n := slugSeq.Add(1)
	return fmt.Sprintf("%se2e%d%d", prefix, time.Now().UnixNano()%1_000_000, n)
}

func registerSite(t *testing.T, e env, slug string) {
	t.Helper()
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/register", e.GHToken,
		map[string]any{"slug": slug, "teams": []string{"staff"}}, nil), http.StatusCreated, "registerSite "+slug)
	waitSiteVisible(t, e, slug)
}

func waitSiteVisible(t *testing.T, e env, slug string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var resp struct {
			AuthorizedSites []string `json:"authorizedSites"`
		}
		if e.call(t, http.MethodGet, "/api/whoami", e.GHToken, nil, &resp) == http.StatusOK {
			if containsString(resp.AuthorizedSites, slug) {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("site %q not visible in whoami authorizedSites within 10s (registry cache propagation)", slug)
}

func deploySHA() string {
	return fmt.Sprintf("%07d", slugSeq.Add(1)%10_000_000)
}

func mintDeploy(t *testing.T, e env, slug, mode string) string {
	t.Helper()
	var initResp struct {
		DeployID string `json:"deployId"`
		JWT      string `json:"jwt"`
	}
	mustStatus(t, e.call(t, http.MethodPost, "/api/deploy/init", e.GHToken,
		map[string]any{"site": slug, "sha": deploySHA(), "files": []string{"index.html"}}, &initResp),
		http.StatusOK, "mintDeploy init")
	mustStatus(t, e.upload(t, initResp.DeployID, initResp.JWT, "index.html", "text/html",
		[]byte("<html>e2e</html>"), nil), http.StatusOK, "mintDeploy upload")
	mustStatus(t, e.call(t, http.MethodPost, fmt.Sprintf("/api/deploy/%s/finalize", initResp.DeployID),
		initResp.JWT, map[string]any{"mode": mode, "files": []string{"index.html"}}, nil),
		http.StatusOK, "mintDeploy finalize")
	return initResp.DeployID
}

func hasPrefix(t *testing.T, c *r2.Client, prefix string) bool {
	t.Helper()
	has, err := c.HasPrefix(context.Background(), prefix)
	if err != nil {
		t.Fatalf("R2 HasPrefix %q: %v", prefix, err)
	}
	return has
}

func waitOutbox(t *testing.T, pool *pgxpool.Pool, site string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox WHERE topic='site.changed' AND payload->>'site'=$1`, site).Scan(&n)
		if err == nil && n >= 1 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("pg outbox: no site.changed row for site=%q within 10s", site)
}

func containsSlug(rows []struct {
	Slug string `json:"slug"`
}, slug string) bool {
	for _, r := range rows {
		if r.Slug == slug {
			return true
		}
	}
	return false
}

func containsID(rows []struct {
	ID string `json:"id"`
}, id string) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
