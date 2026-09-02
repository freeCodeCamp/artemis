//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/testutil/settle"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/r2"
)

var slugSeq atomic.Int64

func uniqueSlug(prefix string) string {
	n := slugSeq.Add(1)
	return fmt.Sprintf("%se2e%d%d", prefix, time.Now().UnixNano()%1_000_000, n)
}

func siteDir(slug string) string {
	return slug + ".e2e.test"
}

func registerSite(t *testing.T, e env, slug string) {
	t.Helper()
	mustStatus(t, e.call(t, http.MethodPost, "/api/site/register", e.GHToken,
		map[string]any{"slug": slug, "teams": []string{"staff"}}, nil), http.StatusCreated, "registerSite "+slug)
	waitSiteVisible(t, e, slug)
}

func waitSiteVisible(t *testing.T, e env, slug string) {
	t.Helper()
	err := settle.Until(t.Context(), 10*time.Second, func(ctx context.Context) (bool, error) {
		status, raw, err := e.raw(ctx, http.MethodGet, "/api/whoami", e.GHToken, nil)
		if err != nil {
			return false, err
		}
		if status != http.StatusOK {
			return false, nil
		}
		var resp struct {
			AuthorizedSites []string `json:"authorizedSites"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return false, err
		}
		return containsString(resp.AuthorizedSites, slug), nil
	}, settle.Every(250*time.Millisecond))
	if err != nil {
		t.Fatalf("site %q not visible in whoami authorizedSites (registry cache propagation): %v", slug, err)
	}
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

func waitTrash(t *testing.T, c *r2.Client, trashPrefix, deployPrefix string) {
	t.Helper()
	err := settle.Until(t.Context(), 90*time.Second, func(ctx context.Context) (bool, error) {
		inTrash, err := c.HasPrefix(ctx, trashPrefix)
		if err != nil {
			return false, err
		}
		stillLive, err := c.HasPrefix(ctx, deployPrefix)
		if err != nil {
			return false, err
		}
		return inTrash && !stillLive, nil
	}, settle.Every(500*time.Millisecond))
	if err != nil {
		t.Fatalf("gc-site did not tombstone-move %q -> %q: %v", deployPrefix, trashPrefix, err)
	}
}

func waitOutbox(t *testing.T, pool *pgxpool.Pool, site string) {
	t.Helper()
	err := settle.Until(t.Context(), 10*time.Second, func(ctx context.Context) (bool, error) {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox WHERE topic='site.changed' AND payload->>'site'=$1`, site).Scan(&n); err != nil {
			return false, err
		}
		return n >= 1, nil
	}, settle.Every(250*time.Millisecond), settle.PerAttempt(5*time.Second))
	if err != nil {
		t.Fatalf("pg outbox: no site.changed row for site=%q: %v", site, err)
	}
}

func containsSlug(rows []struct {
	Slug string `json:"slug"`
}, slug string,
) bool {
	for _, r := range rows {
		if r.Slug == slug {
			return true
		}
	}
	return false
}

func containsID(rows []struct {
	ID string `json:"id"`
}, id string,
) bool {
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

func assertPGAlias(t *testing.T, pool *pgxpool.Pool, site, name, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT deploy_id FROM aliases WHERE site=$1 AND name=$2`, site, name).Scan(&got); err != nil {
		t.Fatalf("pg alias %s/%s: %v", site, name, err)
	}
	if got != want {
		t.Fatalf("pg alias %s/%s=%q want %q", site, name, got, want)
	}
}
