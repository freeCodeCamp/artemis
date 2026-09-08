//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestResponseCacheControl locks the serve-plane Cache-Control contract on
// both preview and production URLs.
//
// The serve plane sends `public, max-age=0, must-revalidate` so the Cloudflare
// edge revalidates every request against the origin. That is what made an
// alias-write edge purge unnecessary: infra `ef71932d` added the header on
// 2026-09-02, and artemis `4eb4c80` then removed the purge seam. See
// docs/COMPATIBILITY.md entry 29.
//
// artemis itself still sets no Cache-Control — `PutObject` sets ContentType
// only — so this asserts the end-to-end value, not an artemis behaviour.
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
			const want = "public, max-age=0, must-revalidate"
			if cc := resp.Header.Get("Cache-Control"); cc != want {
				t.Fatalf("%s: Cache-Control=%q, want %q — the edge must revalidate every request, which is what replaced the alias-write purge. Update this assertion in the commit that changes the header on purpose.",
					tc.url, cc, want)
			}
			t.Logf("[cc] %s status=%d Cache-Control=%q", tc.url, resp.StatusCode, want)
		})
	}
}
