//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestPromoteRace is the positive control for the documented-unsafe
// bare-promote path. With G3 landed, callers have two safe alternatives:
// `{deployId: <id>}` (direct-write, see TestPromoteWithDeployId) and
// `{expectedCurrent: <id>}` (CAS, see TestPromoteCAS). The bare path
// (empty body) is retained for one release behind a `promote.legacy_bare`
// deprecation log (SPEC §V4, T9) and still exhibits the original
// last-writer-wins behavior.
//
// Two operators each finalize a preview deploy back-to-back; operator A
// then bare-POSTs `/api/site/{site}/promote` intending to publish A.
// Because the empty-body branch reads the preview alias body at
// GetAlias time and writes that to prod with no CAS, the published id
// is B's — whoever finalized preview last wins.
//
// This test PINS that contract as the regression-detection trip-wire
// for the legacy code path. The assertion stays `second wins` until
// the bare path is removed entirely (planned follow-up sprint per SPEC
// §V4 / RELEASING.md); when removal lands, this test flips to assert
// `400 Bad Request` and the safe-variant tests carry the contract
// forward.
//
// No R2 creds needed — asserts on the promote HTTP response deployId,
// not on alias body bytes. Suite-level teardown restores baseline prod
// per `setup_teardown_test.go` so this test does not damage the live
// site beyond its run.
func TestPromoteRace(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ts := time.Now().Unix() % 10_000_000

	// Operator A — init + upload + finalize(preview).
	initA := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("rA%07d", ts),
		fmt.Sprintf("A %d", time.Now().UnixNano()))

	// Operator B — back-to-back. Distinct SHA so the deploy IDs
	// differ even if both inits land in the same second.
	initB := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("rB%07d", ts),
		fmt.Sprintf("B %d", time.Now().UnixNano()))

	if initA.DeployID == initB.DeployID {
		t.Fatalf("test setup: both inits returned same deployId %q — distinct SHAs should have produced distinct ids",
			initA.DeployID)
	}
	t.Logf("[race] A=%s B=%s", initA.DeployID, initB.DeployID)

	// Operator A bare-promotes, intending to publish A. The
	// read-then-write reads preview (= B, the later writer) and
	// writes B to prod.
	var promoteResp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken, nil, &promoteResp); err != nil {
		t.Fatalf("bare promote: %v", err)
	}

	if promoteResp.DeployID != initB.DeployID {
		t.Fatalf("bare promote published %q, want B=%q (operator A intended A=%q) — RFC §G B2 contract drift on the documented-unsafe legacy path; use {deployId: A} or {expectedCurrent: <baseline>} for safe variants",
			promoteResp.DeployID, initB.DeployID, initA.DeployID)
	}
	t.Logf("[race] bare promote → %s (= B; legacy path, A's intent silently overridden — see TestPromoteWithDeployId / TestPromoteCAS for safe variants)",
		promoteResp.DeployID)
}

// promoteRaceFinalizePreview runs init → upload → finalize(preview)
// for a single operator slice of a promote-shape test. Shared across
// TestPromoteRace, TestPromoteWithDeployId, and TestPromoteCAS — all
// three exercise the same finalize-then-promote pattern with different
// promote bodies.
//
// Returns the init response (deployId + jwt) so callers can chain
// further calls if they need to (today: not used after finalize).
type initResp struct {
	DeployID  string `json:"deployId"`
	JWT       string `json:"jwt"`
	ExpiresAt string `json:"expiresAt"`
}

func promoteRaceFinalizePreview(
	t *testing.T,
	ctx context.Context,
	c cfg,
	sha, marker string,
) initResp {
	t.Helper()

	// 1. init
	var ir initResp
	if err := c.doJSON(ctx, http.MethodPost, "/api/deploy/init", c.GHToken,
		map[string]any{
			"site":  c.Site,
			"sha":   sha,
			"files": []string{"index.html"},
		}, &ir); err != nil {
		t.Fatalf("init (sha=%s): %v", sha, err)
	}
	registerDeployCleanup(t, ir.DeployID)

	// 2. upload single file
	body := []byte(fmt.Sprintf(
		"<!doctype html><html><body><h1>race-%s</h1></body></html>\n", marker))
	var up struct {
		Received string `json:"received"`
		Key      string `json:"key"`
	}
	if err := c.doUpload(ctx, ir.DeployID, ir.JWT,
		"index.html", "text/html; charset=utf-8", body, &up); err != nil {
		t.Fatalf("upload (deployId=%s): %v", ir.DeployID, err)
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
		map[string]any{"mode": "preview", "files": []string{"index.html"}},
		&fin); err != nil {
		t.Fatalf("finalize preview (deployId=%s): %v", ir.DeployID, err)
	}
	if fin.DeployID != ir.DeployID {
		t.Fatalf("finalize echo deployId=%q, want %q", fin.DeployID, ir.DeployID)
	}
	return ir
}
