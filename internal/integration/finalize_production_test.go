//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestDeployPromoteSkipsPreview pins the B6/B4b contract:
// `finalize(mode: production)` writes alias-by-mode (handler/deploy.go:207-211)
// — it touches the production alias key only. The preview alias is
// untouched.
//
// This is the artemis behavior that surfaces operator-visibly as
// "universe static deploy --promote leaves preview lagging prod."
// universe-cli `static.deploy --promote` flips `mode: "production"`
// in the /finalize body and does not also call /api/site/{site}/promote.
// The CLI-side fake-artemis test (universe-cli
// tests/e2e/deploy-production.test.ts) asserts this from the client
// side; T3 asserts it from the live R2 side, against the real artemis.
//
// Test shape:
//
//  1. Snapshot preview alias body (string or NotFound — site may be fresh).
//  2. New deployId via init + upload of a fresh marker.
//  3. finalize(mode: production) — single shot, no preview pass.
//  4. Assert R2 prod alias body == new deployId.
//  5. Assert R2 preview alias body == snapshot (or still NotFound).
//
// Requires R2_* env (uses r2Client helper). Skips if absent.
// Suite-level teardown restores baseline prod after run.
//
// If T3 ever flips ("preview alias mutated by finalize(production)"),
// either artemis grew a multi-alias finalize (G3 follow-up) or
// something quietly went wrong. Either case, this is the trip-wire.
func TestDeployPromoteSkipsPreview(t *testing.T) {
	c := loadCfg(t)
	r := r2Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	previewKey := aliasKey(c, "preview")
	prodKey := aliasKey(c, "production")

	// 1. Snapshot preview baseline.
	previewBefore, errBefore := r.getAlias(ctx, previewKey)
	freshSite := errors.Is(errBefore, errAliasNotFound)
	switch {
	case freshSite:
		t.Logf("[skip-preview] baseline: preview alias absent (fresh site)")
	case errBefore != nil:
		t.Fatalf("baseline getAlias %s: %v", previewKey, errBefore)
	default:
		t.Logf("[skip-preview] baseline: preview alias = %q", previewBefore)
	}

	// 2. New deployId via init + upload.
	ts := time.Now().Unix() % 10_000_000
	sha := fmt.Sprintf("sp%07d", ts)

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
	t.Logf("[skip-preview] new deployId=%s", ir.DeployID)

	body := []byte(fmt.Sprintf(
		"<!doctype html><html><body>skip-preview %s</body></html>\n", ir.DeployID))
	var up struct {
		Received string `json:"received"`
		Key      string `json:"key"`
	}
	if err := c.doUpload(ctx, ir.DeployID, ir.JWT,
		"index.html", "text/html; charset=utf-8", body, &up); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// 3. finalize(mode: production) — direct, no preview pass.
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

	// 4. Prod alias body must equal the new deployId.
	prodAfter, err := r.getAlias(ctx, prodKey)
	if err != nil {
		t.Fatalf("getAlias %s: %v", prodKey, err)
	}
	if prodAfter != ir.DeployID {
		t.Fatalf("production alias body=%q want %q (raw bytes)",
			prodAfter, ir.DeployID)
	}
	t.Logf("[skip-preview] production alias body=%q ✓", prodAfter)

	// 5. Preview alias must be unchanged from snapshot.
	previewAfter, errAfter := r.getAlias(ctx, previewKey)
	switch {
	case freshSite:
		if !errors.Is(errAfter, errAliasNotFound) {
			t.Fatalf("preview alias unexpectedly written by finalize(production): body=%q err=%v (expected NotFound on fresh site)",
				previewAfter, errAfter)
		}
		t.Logf("[skip-preview] preview alias still absent ✓ (B6 contract holds)")
	case errAfter != nil:
		t.Fatalf("post getAlias %s: %v", previewKey, errAfter)
	default:
		if previewAfter != previewBefore {
			t.Fatalf("preview alias mutated by finalize(production): before=%q after=%q\n"+
				"  → B6 contract broken; finalize(production) is no longer alias-by-mode-only",
				previewBefore, previewAfter)
		}
		t.Logf("[skip-preview] preview alias unchanged ✓ (B6 contract holds: %q)", previewAfter)
	}
}
