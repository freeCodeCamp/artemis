//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The Gateway URLRewrite is configured in
// k3s/gxy-management/apps/artemis/charts/artemis/templates/httproute.yaml.
func TestPublicRouteBlock(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("readyz_returns_404", func(t *testing.T) {
		status, body := c.statusOnly(ctx, http.MethodGet, "/readyz", "", nil)
		if status != 404 {
			t.Fatalf("public /readyz: status=%d body=%s — want 404 (Gateway URLRewrite to /_artemis_blocked_path)", status, truncate(body, 200))
		}
	})

	t.Run("healthz_stays_public_200", func(t *testing.T) {
		status, body := c.statusOnly(ctx, http.MethodGet, "/healthz", "", nil)
		if status != 200 {
			t.Fatalf("public /healthz: status=%d body=%s — want 200 (no rewrite, documented external liveness probe)", status, truncate(body, 200))
		}
	})

	t.Run("rewrite_target_returns_404", func(t *testing.T) {
		status, body := c.statusOnly(ctx, http.MethodGet, "/_artemis_blocked_path", "", nil)
		if status != 404 {
			t.Fatalf("/_artemis_blocked_path: status=%d body=%s — want 404 (chi catch-all on unknown path)", status, truncate(body, 200))
		}
	})
}
