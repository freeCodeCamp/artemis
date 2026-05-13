//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Asserts the direct-write path (SPEC §V2) does not silently fall
// back to the legacy read-preview branch. HTTP-only — no R2 creds.
func TestPromoteWithDeployId(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ts := time.Now().Unix() % 10_000_000

	initA := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("dA%07d", ts),
		fmt.Sprintf("A %d", time.Now().UnixNano()))

	initB := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("dB%07d", ts),
		fmt.Sprintf("B %d", time.Now().UnixNano()))

	if initA.DeployID == initB.DeployID {
		t.Fatalf("setup: both inits returned %q", initA.DeployID)
	}
	t.Logf("[direct-write] A=%s B=%s (preview=B)", initA.DeployID, initB.DeployID)

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
		t.Fatalf("promote published %q, want A=%q (preview=B=%q — handler fell back to legacy read-preview; §V2)",
			promoteResp.DeployID, initA.DeployID, initB.DeployID)
	}
	t.Logf("[direct-write] promote {deployId: A} → %s", promoteResp.DeployID)
}
