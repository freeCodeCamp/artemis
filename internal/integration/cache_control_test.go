//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestResponseCacheControl locks the present-day no-Cache-Control
// contract on both preview and production URLs.
//
// End-to-end path: artemis `PutObject` (internal/r2/r2.go:80-97) sets
// `ContentType` only — never `CacheControl`. Caddy `file_server` reads
// the R2 object via the `r2_alias` plugin (configured by the operator's
// reverse-proxy chart) and synthesizes no Cache-Control. CF zone-default
// does not add one to HTML. Result: `curl -I https://<site>.<root>/`
// shows no `cache-control` response header today.
//
// This test is a trip-wire. When any layer ships explicit
// Cache-Control (defensive Caddy header per RFC §G "Defer" row, or
// future PutObject change in artemis, or a CF page rule), update the
// expected value here in the same commit. Until then, this stays the
// canonical baseline assertion.
//
// No R2 creds needed — pure HTTP HEAD.
func TestResponseCacheControl(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cases := []struct {
		name string
		url  string
	}{
		{"production", fmt.Sprintf("https://%s.%s/", c.Site, c.RootDomain)},
		{"preview", fmt.Sprintf("https://%s.preview.%s/", c.Site, c.RootDomain)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, tc.url, nil)
			if err != nil {
				t.Fatalf("new req: %v", err)
			}
			resp, err := c.HTTP.Do(req)
			if err != nil {
				t.Fatalf("HEAD %s: %v", tc.url, err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Skipf("HEAD %s returned %d; expected 200 (site %s has no %s deploy yet?)",
					tc.url, resp.StatusCode, c.Site, tc.name)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "" {
				t.Fatalf("%s: Cache-Control=%q — baseline contract is absent end-to-end (artemis PutObject sets ContentType only; Caddy file_server synthesizes none; CF zone-default does not add one to HTML). Update this assertion in the commit that adds Cache-Control on purpose.",
					tc.url, cc)
			}
			t.Logf("[cc] %s status=%d Cache-Control=<absent>", tc.url, resp.StatusCode)
		})
	}
}
