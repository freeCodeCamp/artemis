//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestSubtractivePromote pins the empirical 2026-05-12 finding: a
// later preview deploy that drops a file the prior preview carried,
// then a bare promote, silently 404s the dropped file on prod. The
// operator never typed "delete F1" but F1 disappears.
//
// Mechanism (handler/site.go:15-47 + Caddy r2_alias):
//
//  1. finalize(preview, [index.html, F1.html]) — preview alias points
//     at deploy A which has both files.
//  2. bare promote — prod alias = A. `/F1.html` serves A's bytes.
//  3. finalize(preview, [index.html]) — preview alias points at
//     deploy B which has only index.html. F1.html is not in B's R2
//     prefix.
//  4. bare promote — prod alias = B. Caddy reads alias body B,
//     resolves `/F1.html` to `<bucket>/<site>/deploys/B/F1.html`,
//     gets NoSuchKey, surfaces a 404. Operator's `F1.html` is gone
//     from prod even though they never deleted it.
//
// Verification waits for two Caddy cache + CF settle windows (one
// after each promote) — `pollForMarker` (existing) for promote A's
// presence, `pollFor404` (new, defined below) for promote B's
// subtractive effect. Each settle is bounded by `c.ProdSLO` (default
// 2m, override via `PROD_SLO` env).
//
// No R2 creds needed — assertions are HTTP only.
//
// Slow test (60-90s in practice) — gated under `make integration`
// like every other integration test. Suite-level teardown restores
// baseline prod after the test runs.
func TestSubtractivePromote(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	ts := time.Now().Unix() % 10_000_000
	runMarker := fmt.Sprintf("subtractive-%d", time.Now().UnixNano())

	// 1. Preview A — index.html + F1.html.
	initA := finalizePreviewMulti(t, ctx, c,
		fmt.Sprintf("sA%07d", ts),
		map[string][]byte{
			"index.html": []byte(fmt.Sprintf(
				"<!doctype html><html><body>A %s</body></html>\n", runMarker)),
			"F1.html": []byte(fmt.Sprintf(
				"<!doctype html><html><body>F1 %s</body></html>\n", runMarker)),
		})
	t.Logf("[subtract] A deploy=%s (index + F1)", initA.DeployID)

	// 2. Bare-promote A → F1 lands on prod.
	var promoteA struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken, nil, &promoteA); err != nil {
		t.Fatalf("promote A: %v", err)
	}
	if promoteA.DeployID != initA.DeployID {
		t.Fatalf("promote A: deployId=%q want %q", promoteA.DeployID, initA.DeployID)
	}

	f1URL := fmt.Sprintf("https://%s.%s/F1.html", c.Site, c.RootDomain)
	t.Logf("[subtract] promote A done; polling %s for F1 marker (budget=%s)",
		f1URL, c.ProdSLO)
	if err := c.pollForMarker(t, f1URL,
		fmt.Sprintf("F1 %s", runMarker), c.ProdSLO); err != nil {
		t.Fatalf("F1 should serve from prod after promote A: %v", err)
	}

	// 3. Preview B — only index.html. F1.html intentionally omitted.
	initB := finalizePreviewMulti(t, ctx, c,
		fmt.Sprintf("sB%07d", ts),
		map[string][]byte{
			"index.html": []byte(fmt.Sprintf(
				"<!doctype html><html><body>B %s</body></html>\n", runMarker)),
		})
	t.Logf("[subtract] B deploy=%s (index only, F1 dropped)", initB.DeployID)

	// 4. Bare-promote B → prod = B. F1.html must 404.
	var promoteB struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken, nil, &promoteB); err != nil {
		t.Fatalf("promote B: %v", err)
	}
	if promoteB.DeployID != initB.DeployID {
		t.Fatalf("promote B: deployId=%q want %q", promoteB.DeployID, initB.DeployID)
	}

	t.Logf("[subtract] promote B done; polling %s for 404 (budget=%s)",
		f1URL, c.ProdSLO)
	if err := pollFor404(t, c, f1URL, c.ProdSLO); err != nil {
		t.Fatalf("F1 should 404 after promote B (subtractive contract): %v", err)
	}
	t.Logf("[subtract] OK — F1 silently 404d on prod after operator dropped it from preview B; RFC §G B4 contract pinned")
}

// finalizePreviewMulti runs init → upload (one PUT per file) →
// finalize(preview) for a multi-file deploy. Returns the init
// response so callers can chain. The `files` map keys are relative
// upload paths (e.g. "index.html", "F1.html"); values are the raw
// bytes for each.
//
// Multi-file extension of `promoteRaceFinalizePreview` from
// promote_race_test.go — kept inline here rather than extracted to
// preserve one-commit-per-task boundaries; refactor candidate for
// the T2/T3 commits.
func finalizePreviewMulti(
	t *testing.T,
	ctx context.Context,
	c cfg,
	sha string,
	files map[string][]byte,
) initResp {
	t.Helper()
	if len(files) == 0 {
		t.Fatalf("finalizePreviewMulti: files must be non-empty")
	}

	// 1. init — declare every file up-front so VerifyDeployComplete
	// matches the upload set on finalize.
	pathList := make([]string, 0, len(files))
	for p := range files {
		pathList = append(pathList, p)
	}

	var ir initResp
	if err := c.doJSON(ctx, http.MethodPost, "/api/deploy/init", c.GHToken,
		map[string]any{
			"site":  c.Site,
			"sha":   sha,
			"files": pathList,
		}, &ir); err != nil {
		t.Fatalf("init (sha=%s): %v", sha, err)
	}
	registerDeployCleanup(t, ir.DeployID)

	// 2. upload one PUT per file
	for path, body := range files {
		var up struct {
			Received string `json:"received"`
			Key      string `json:"key"`
		}
		if err := c.doUpload(ctx, ir.DeployID, ir.JWT,
			path, "text/html; charset=utf-8", body, &up); err != nil {
			t.Fatalf("upload (deployId=%s path=%s): %v", ir.DeployID, path, err)
		}
		if up.Received != path {
			t.Fatalf("upload echo: received=%q want %q", up.Received, path)
		}
	}

	// 3. finalize(preview)
	var fin struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
		Mode     string `json:"mode"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/deploy/%s/finalize", ir.DeployID),
		ir.JWT,
		map[string]any{"mode": "preview", "files": pathList},
		&fin); err != nil {
		t.Fatalf("finalize preview (deployId=%s): %v", ir.DeployID, err)
	}
	if fin.DeployID != ir.DeployID {
		t.Fatalf("finalize echo deployId=%q want %q", fin.DeployID, ir.DeployID)
	}
	return ir
}

// pollFor404 polls url until two consecutive GETs return 404 (matches
// the `pollForMarker` "settle on two consecutive hits" rule). Network
// errors and non-404 statuses reset the streak. Returns nil on
// success, an error if the budget expires.
//
// Mirror of `pollForMarker` shape (proxy_e2e_test.go:240-275) but
// inverted: we are waiting for absence rather than presence.
func pollFor404(t *testing.T, c cfg, url string, budget time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(budget)
	consecutive := 0
	const consecutiveTarget = 2
	const interval = 5 * time.Second

	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(10*time.Second))
	defer cancel()

	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Cache-Control", "no-cache")
		resp, err := c.HTTP.Do(req)
		switch {
		case err != nil:
			t.Logf("[poll-404] %s: err=%v (streak reset)", url, err)
			consecutive = 0
		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			consecutive++
			t.Logf("[poll-404] %s: status=404 (streak=%d/%d)",
				url, consecutive, consecutiveTarget)
			if consecutive >= consecutiveTarget {
				return nil
			}
		default:
			resp.Body.Close()
			t.Logf("[poll-404] %s: status=%d (streak reset)",
				url, resp.StatusCode)
			consecutive = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll cancelled: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("404 not seen on %d consecutive hits within %s",
		consecutiveTarget, budget)
}
