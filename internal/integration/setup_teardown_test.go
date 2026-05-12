//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// baselineProdDeployID is the production-alias deploy id observed at
// suite start. After all tests run, TestMain rewires production back
// to this deploy id so the suite is alias-idempotent — tests can
// freely promote new content during the run without leaving the live
// site pinned to a synthetic deploy.
//
// Empty string means baseline capture failed or no prior production
// deploy existed (fresh site). In either case, teardown is a no-op.
var baselineProdDeployID string

// TestMain wires suite-level setup + teardown. Order:
//
//  1. Resolve env (skip baseline capture if ARTEMIS_URL or GH_TOKEN
//     unset — individual tests Skip themselves).
//  2. Pre-flight healthz — fail fast on a dead artemis instead of
//     letting every test hang on its own timeout.
//  3. Capture baseline prod deploy id for SITE.
//  4. m.Run() — execute the suite.
//  5. Restore prod alias to baseline (best effort; logs warnings, does
//     not fail the suite).
func TestMain(m *testing.M) {
	artemisURL := strings.TrimRight(os.Getenv("ARTEMIS_URL"), "/")
	ghToken := os.Getenv("GH_TOKEN")
	site := envDefault("SITE", "test")

	if artemisURL == "" || ghToken == "" {
		log.Printf("[setup] ARTEMIS_URL or GH_TOKEN unset — tests will Skip; no baseline capture")
		os.Exit(m.Run())
	}

	suiteCfg := cfg{
		ArtemisURL: artemisURL,
		GHToken:    ghToken,
		Site:       site,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}

	if err := preflightHealthz(suiteCfg); err != nil {
		log.Printf("[setup] FATAL: healthz pre-flight failed: %v", err)
		log.Printf("[setup]        artemis at %s appears unreachable; aborting before any test runs", artemisURL)
		os.Exit(2)
	}
	log.Printf("[setup] healthz green at %s", artemisURL)

	id, err := captureBaselineProd(suiteCfg)
	switch {
	case err != nil:
		log.Printf("[setup] WARN: baseline capture failed: %v (teardown will be no-op)", err)
	case id == "":
		log.Printf("[setup] no prior prod deploy for site=%s (fresh site; teardown will be no-op)", site)
	default:
		log.Printf("[setup] captured baseline: site=%s deployId=%s", site, id)
		baselineProdDeployID = id
	}

	code := m.Run()

	if baselineProdDeployID != "" {
		if err := restoreProd(suiteCfg, baselineProdDeployID); err != nil {
			log.Printf("[teardown] WARN: restore prod alias failed: %v", err)
			log.Printf("[teardown]           prod alias for site=%s may be left pinned to a test deploy",
				site)
			log.Printf("[teardown]           manual fix: POST /api/site/%s/rollback {\"to\":\"%s\"}",
				site, baselineProdDeployID)
		} else {
			log.Printf("[teardown] restored prod alias: site=%s deployId=%s",
				site, baselineProdDeployID)
		}
	}

	os.Exit(code)
}

// preflightHealthz hits /healthz once with a short timeout. Returns
// nil on 2xx, error otherwise.
func preflightHealthz(c cfg) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/healthz", "", nil, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("healthz returned ok=false")
	}
	return nil
}

// captureBaselineProd reads the deploys list and returns the most
// recent deploy id, which is what `/api/site/{site}/promote` would
// most plausibly have made live last. Used by teardown to leave the
// live site converged on the most recent deploy rather than reverting
// to the oldest. Empty string + nil error means the site has no
// deploys yet (fresh site).
//
// Order note: `/api/site/{site}/deploys` returns deploys in R2
// ListObjectsV2 lex-ascending order (handler/site.go:99-129 does no
// post-sort). Deploy IDs are `<yyyymmdd-hhmmss>-<sha7>` so lex-ascending
// = chronological-ascending = oldest-first. The newest is therefore
// `deploys[len-1]`, not `deploys[0]`. universe-cli wraps this list
// with a client-side reverse before display (see f746a01); the raw
// API does not. Pre-2026-05-12 this helper read `deploys[0]` and the
// teardown was reverting prod toward the oldest deploy every run.
func captureBaselineProd(c cfg) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var deploys []struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/api/site/%s/deploys", c.Site),
		c.GHToken, nil, &deploys); err != nil {
		return "", fmt.Errorf("list deploys: %w", err)
	}
	if len(deploys) == 0 {
		return "", nil
	}
	return deploys[len(deploys)-1].DeployID, nil
}

// restoreProd POSTs /api/site/{site}/rollback to rewire the prod
// alias to the supplied deployId.
func restoreProd(c cfg, deployID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var resp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/rollback", c.Site),
		c.GHToken, map[string]any{"to": deployID}, &resp); err != nil {
		return err
	}
	if resp.DeployID != deployID {
		return fmt.Errorf("rollback echoed deployId=%q, want %q", resp.DeployID, deployID)
	}
	return nil
}

// registerDeployCleanup logs (at test end, success or failure) the
// deploy id any test created. Cleanup cron (T22, 7-day retention)
// sweeps the prefix; this just makes the artifact visible in test
// output for debugging.
//
// Used by tests that mint a new deploy (TestDeployFlow). t.Cleanup
// runs in LIFO order — register early so it fires last.
func registerDeployCleanup(t *testing.T, deployID string) {
	t.Helper()
	t.Cleanup(func() {
		t.Logf("[cleanup] deploy %s left in R2 for site=%s — cleanup cron (T22, 7d retention) will sweep",
			deployID, envDefault("SITE", "test"))
	})
}
