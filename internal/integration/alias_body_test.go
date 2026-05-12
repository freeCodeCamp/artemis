//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestAliasBodyRoundTrip proves the bytes artemis says it wrote
// actually landed in R2 — the failure mode the HTTP API alone cannot
// detect (the API just echoes the deployId it was given; nothing in
// the response proves the alias body matches).
//
// Three failure modes this test catches that nothing else does:
//
//  1. Whitespace drift — PutAlias inadvertently appends `\n` (e.g. an
//     `Fprintln` slips in). API still returns the deployId; bare
//     promote later reads back the body with the stray byte and
//     writes that to prod. Caddy's r2_alias trims, so live serving
//     stays green; the drift compounds silently across promotes.
//  2. Wrong key path — alias format env / code drifts so artemis
//     writes to a non-canonical key (e.g. `<site>/preview` instead of
//     `<site>.freecode.camp/preview`). API succeeds; canonical key
//     stays stale.
//  3. Silent PutAlias no-op — SDK call returns nil but the object
//     doesn't land (transient R2 partition, mocked Client, etc.).
//     API returns the deployId; the alias body is stale.
//
// Strict byte equality on purpose. Do NOT `TrimSpace` before
// comparing — that would mask failure mode (1). If T2 ever flakes on
// whitespace, that is the signal, not a flake.
//
// Requires R2_* env (uses r2Client helper). Skips if absent.
// Suite-level teardown restores baseline prod after run.
func TestAliasBodyRoundTrip(t *testing.T) {
	c := loadCfg(t)
	r := r2Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ts := time.Now().Unix() % 10_000_000
	sha := fmt.Sprintf("rt%07d", ts)

	// 1. init
	var ir initResp
	if err := c.doJSON(ctx, http.MethodPost, "/api/deploy/init", c.GHToken,
		map[string]any{
			"site":  c.Site,
			"sha":   sha,
			"files": []string{"index.html"},
		}, &ir); err != nil {
		t.Fatalf("init: %v", err)
	}
	registerDeployCleanup(t, ir.DeployID)
	t.Logf("[round-trip] deployId=%s", ir.DeployID)

	// 2. upload
	body := []byte(fmt.Sprintf(
		"<!doctype html><html><body>round-trip %s</body></html>\n", ir.DeployID))
	var up struct {
		Received string `json:"received"`
		Key      string `json:"key"`
	}
	if err := c.doUpload(ctx, ir.DeployID, ir.JWT,
		"index.html", "text/html; charset=utf-8", body, &up); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 3. finalize(preview) — same deployId
	var finPrev struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/deploy/%s/finalize", ir.DeployID), ir.JWT,
		map[string]any{"mode": "preview", "files": []string{"index.html"}},
		&finPrev); err != nil {
		t.Fatalf("finalize preview: %v", err)
	}
	if finPrev.DeployID != ir.DeployID {
		t.Fatalf("finalize preview echo deployId=%q want %q",
			finPrev.DeployID, ir.DeployID)
	}

	// 4. Direct R2 read of preview alias key — strict byte equality.
	previewKey := aliasKey(c, "preview")
	previewBody, err := r.getAlias(ctx, previewKey)
	if err != nil {
		t.Fatalf("getAlias %s: %v", previewKey, err)
	}
	if previewBody != ir.DeployID {
		t.Fatalf("preview alias body=%q want %q (raw bytes — no TrimSpace)\n"+
			"  → check artemis PutAlias for whitespace drift, key-format drift, or silent no-op",
			previewBody, ir.DeployID)
	}
	t.Logf("[round-trip] preview  alias %s body=%q ✓", previewKey, previewBody)

	// 5. finalize(production) — same deployId, same JWT. Tests the
	// "deploy then promote" semantics without going through the bare
	// /promote route (which reads preview, writes prod).
	var finProd struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/deploy/%s/finalize", ir.DeployID), ir.JWT,
		map[string]any{"mode": "production", "files": []string{"index.html"}},
		&finProd); err != nil {
		t.Fatalf("finalize production: %v", err)
	}
	if finProd.DeployID != ir.DeployID {
		t.Fatalf("finalize production echo deployId=%q want %q",
			finProd.DeployID, ir.DeployID)
	}

	// 6. Direct R2 read of production alias key — strict byte equality.
	prodKey := aliasKey(c, "production")
	prodBody, err := r.getAlias(ctx, prodKey)
	if err != nil {
		t.Fatalf("getAlias %s: %v", prodKey, err)
	}
	if prodBody != ir.DeployID {
		t.Fatalf("production alias body=%q want %q (raw bytes — no TrimSpace)\n"+
			"  → check artemis PutAlias for whitespace drift, key-format drift, or silent no-op",
			prodBody, ir.DeployID)
	}
	t.Logf("[round-trip] production alias %s body=%q ✓", prodKey, prodBody)
}
