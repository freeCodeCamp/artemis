//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestPromoteWithDeployId proves the direct-write promote path
// (`{deployId: <id>}` body) bypasses the preview-alias read and
// deterministically publishes the supplied id — even when a later
// finalize has already moved the preview alias to a different deploy.
//
// Shape mirrors TestPromoteRace so the safe/unsafe contracts read
// side-by-side in the suite:
//
//  1. Operator A finalize(preview) → preview alias = A.
//  2. Operator B finalize(preview) → preview alias = B (last writer
//     wins; legacy bare-promote would publish B).
//  3. Operator A POSTs /promote with body `{deployId: A}` — explicit
//     intent. The handler must skip GetAlias(preview) entirely
//     (SPEC §V2) and PUT A into the production alias.
//
// Assertion: the promote response echoes A, NOT B. The contradicting
// case (echoes B) means the direct-write branch silently fell back to
// the legacy read-preview path — the regression this test exists to
// catch.
//
// No R2 creds needed — asserts on the promote HTTP response only.
// Suite-level teardown restores baseline prod per
// setup_teardown_test.go so the live site is not left pinned to a
// synthetic deploy beyond the run.
func TestPromoteWithDeployId(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ts := time.Now().Unix() % 10_000_000

	// Operator A — init + upload + finalize(preview).
	initA := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("dA%07d", ts),
		fmt.Sprintf("A %d", time.Now().UnixNano()))

	// Operator B — back-to-back. Preview alias now points at B.
	initB := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("dB%07d", ts),
		fmt.Sprintf("B %d", time.Now().UnixNano()))

	if initA.DeployID == initB.DeployID {
		t.Fatalf("test setup: both inits returned same deployId %q — distinct SHAs should have produced distinct ids",
			initA.DeployID)
	}
	t.Logf("[direct-write] A=%s B=%s (preview alias now pinned to B)",
		initA.DeployID, initB.DeployID)

	// Operator A promotes with explicit intent: publish A, not the
	// current preview pointer. Direct-write path must skip the
	// preview-alias read entirely (SPEC §V2).
	var promoteResp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken,
		map[string]any{"deployId": initA.DeployID},
		&promoteResp); err != nil {
		t.Fatalf("promote {deployId: A}: %v", err)
	}

	if promoteResp.DeployID != initA.DeployID {
		t.Fatalf("direct-write promote published %q, want A=%q (B=%q is the current preview — handler must NOT have fallen back to the legacy read-preview path; SPEC §V2 violated)",
			promoteResp.DeployID, initA.DeployID, initB.DeployID)
	}
	t.Logf("[direct-write] promote {deployId: A} → %s (preview=B ignored, V2 holds)",
		promoteResp.DeployID)
}
