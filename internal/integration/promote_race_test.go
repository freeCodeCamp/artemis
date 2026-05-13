//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestPromoteRead pins the bare-promote read-then-write contract that
// motivated the RFC §G investigation. Two operators each finalize a
// preview deploy back-to-back, then operator A calls bare
// `/api/site/{site}/promote` expecting to publish A's deploy. Because
// `SitePromote` (handler/site.go:15-47) reads the preview alias body
// at GetAlias time and writes that to prod with no CAS, what actually
// gets published is B's deploy id — whoever finalized last wins.
//
// This test codifies the current behavior as a regression-detection
// trip-wire. If `SitePromote` ever grows CAS body params (G3 follow-up
// — `expectedCurrent` refuses on mismatch), the assertion flips from
// "second wins" to "API refused with a conflict code"; T4 is the
// natural place to make that flip.
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
		t.Fatalf("bare promote published %q, want B=%q (operator A intended A=%q) — RFC §G B2 contract drift",
			promoteResp.DeployID, initB.DeployID, initA.DeployID)
	}
	t.Logf("[race] bare promote → %s (= B, A's intent silently overridden)",
		promoteResp.DeployID)
}

// promoteRaceFinalizePreview runs init → upload → finalize(preview)
// for a single operator slice of TestPromoteRace. Inlined-pattern
// matching TestDeployFlow (no new abstractions yet — extract if T2/T3/T5
// want the same shape).
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
