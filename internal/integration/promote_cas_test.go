//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// Pins CAS guard (SPEC §V3): same expectedCurrent must yield 200 once
// then 409 alias_drift with current=<new prod> on the racing second
// promote. HTTP-only — no R2 creds.
func TestPromoteCAS(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var baselineResp struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/api/site/%s/alias/production", c.Site),
		c.GHToken, nil, &baselineResp); err != nil {
		t.Skipf("baseline read (need prior prod deploy on site=%s): %v", c.Site, err)
	}
	baseline := baselineResp.DeployID
	if baseline == "" {
		t.Skipf("baseline alias body empty on site=%s", c.Site)
	}
	t.Logf("[cas] baseline=%s", baseline)

	ts := time.Now().Unix() % 10_000_000
	initA := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("cA%07d", ts),
		fmt.Sprintf("A %d", time.Now().UnixNano()))
	initB := promoteRaceFinalizePreview(t, ctx, c,
		fmt.Sprintf("cB%07d", ts),
		fmt.Sprintf("B %d", time.Now().UnixNano()))
	if initA.DeployID == initB.DeployID {
		t.Fatalf("setup: both inits returned %q", initA.DeployID)
	}

	var ok struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken,
		map[string]any{"deployId": initA.DeployID, "expectedCurrent": baseline},
		&ok); err != nil {
		t.Fatalf("promote A (CAS match): %v", err)
	}
	if ok.DeployID != initA.DeployID {
		t.Fatalf("promote A echoed %q, want %q", ok.DeployID, initA.DeployID)
	}
	t.Logf("[cas] promote A {expectedCurrent: baseline} → 200 %s", ok.DeployID)

	status, body := c.statusOnly(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken,
		map[string]any{"deployId": initB.DeployID, "expectedCurrent": baseline})
	if status != http.StatusConflict {
		t.Fatalf("racing promote B status=%d body=%s, want 409", status, body)
	}
	var drift struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Site    string `json:"site"`
		Current string `json:"current"`
	}
	if err := json.Unmarshal(body, &drift); err != nil {
		t.Fatalf("decode 409 body: %v; raw=%s", err, body)
	}
	if drift.Error.Code != "alias_drift" {
		t.Fatalf("409 error.code=%q, want alias_drift; raw=%s", drift.Error.Code, body)
	}
	if drift.Current != initA.DeployID {
		t.Fatalf("409 current=%q, want A=%q (server should report the now-current prod); raw=%s",
			drift.Current, initA.DeployID, body)
	}
	t.Logf("[cas] promote B {expectedCurrent: baseline} → 409 alias_drift current=%s ✓", drift.Current)
}
