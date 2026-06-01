//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// cfg holds the shared integration-suite configuration resolved from env.
type cfg struct {
	ArtemisURL string
	GHToken    string
	RootDomain string
	Site       string
	HTTP       *http.Client
	PreviewSLO time.Duration
	ProdSLO    time.Duration
}

const skipUsage = `

Integration suite skipped: %s not set.

To enable, export:

  ARTEMIS_URL=https://uploads.freecode.camp
  GH_TOKEN=$(gh auth token)
  SITE=test
  ROOT_DOMAIN=freecode.camp

Then re-run with:

  go test -v -tags=integration ./internal/integration/...
  # or
  just integration
`

func loadCfg(t *testing.T) cfg {
	t.Helper()
	url := strings.TrimRight(os.Getenv("ARTEMIS_URL"), "/")
	if url == "" {
		t.Skipf(skipUsage, "ARTEMIS_URL")
	}
	tok := os.Getenv("GH_TOKEN")
	if tok == "" {
		t.Skipf(skipUsage, "GH_TOKEN")
	}
	timeout := envDuration("HTTP_TIMEOUT", 30*time.Second)
	return cfg{
		ArtemisURL: url,
		GHToken:    tok,
		RootDomain: envDefault("ROOT_DOMAIN", "freecode.camp"),
		Site:       envDefault("SITE", "test"),
		HTTP:       &http.Client{Timeout: timeout},
		PreviewSLO: envDuration("PREVIEW_SLO", 90*time.Second),
		ProdSLO:    envDuration("PROD_SLO", 2*time.Minute),
	}
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// --------------------------------------------------------------------
// HTTP helpers — minimal, dependency-free.
// --------------------------------------------------------------------

// doJSON runs an artemis API call. Marshals reqBody as JSON (or sends
// no body if nil), parses the response into respBody (or discards if
// nil). Returns a typed error on non-2xx that includes the artemis
// envelope code+message.
func (c cfg) doJSON(ctx context.Context, method, path, bearer string, reqBody, respBody any) error {
	var rdr io.Reader
	if reqBody != nil {
		buf, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal req: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.ArtemisURL+path, rdr)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.send(req, respBody)
}

// doUpload runs PUT /api/deploy/{id}/upload?path=... with raw bytes.
func (c cfg) doUpload(ctx context.Context, deployID, jwt, relPath, contentType string, body []byte, respBody any) error {
	url := fmt.Sprintf("%s/api/deploy/%s/upload?path=%s",
		c.ArtemisURL, deployID, relPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	req.ContentLength = int64(len(body))
	return c.send(req, respBody)
}

// statusOnly returns the bare HTTP status (and body) without failing
// on non-2xx — used by the negative auth tests.
func (c cfg) statusOnly(ctx context.Context, method, path, bearer string, reqBody any) (int, []byte) {
	var rdr io.Reader
	if reqBody != nil {
		buf, _ := json.Marshal(reqBody)
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.ArtemisURL+path, rdr)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func (c cfg) send(req *http.Request, respBody any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, raw)
	}
	if respBody == nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, respBody); err != nil {
		return fmt.Errorf("decode resp (status=%d): %w; body=%s",
			resp.StatusCode, err, truncate(raw, 200))
	}
	return nil
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("artemis %d %s: %s", e.Status, e.Code, e.Message)
}

func parseAPIError(status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		return &apiError{Status: status, Code: env.Error.Code, Message: env.Error.Message}
	}
	return &apiError{
		Status:  status,
		Code:    fmt.Sprintf("http_%d", status),
		Message: string(truncate(body, 200)),
	}
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	out := make([]byte, n+3)
	copy(out, b[:n])
	copy(out[n:], "...")
	return out
}

// fetchAndContains GETs url and returns true iff the body contains
// marker. Network/HTTP errors return false (caller polls).
func (c cfg) fetchAndContains(ctx context.Context, url, marker string) (bool, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return bytes.Contains(body, []byte(marker)), resp.StatusCode, nil
}

// pollForMarker polls url until body contains marker on two
// consecutive hits (alias-cache settling rule) or budget expires.
// Returns total wait + error.
func (c cfg) pollForMarker(t *testing.T, url, marker string, budget time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(budget)
	consecutive := 0
	const consecutiveTarget = 2
	const interval = 5 * time.Second

	ctx, cancel := context.WithDeadline(context.Background(), deadline.Add(10*time.Second))
	defer cancel()

	for time.Now().Before(deadline) {
		hit, status, err := c.fetchAndContains(ctx, url, marker)
		switch {
		case err != nil:
			t.Logf("[poll] %s: err=%v (resetting streak)", url, err)
			consecutive = 0
		case hit:
			consecutive++
			t.Logf("[poll] %s: status=%d hit (streak=%d/%d)", url, status, consecutive, consecutiveTarget)
			if consecutive >= consecutiveTarget {
				return nil
			}
		default:
			t.Logf("[poll] %s: status=%d miss (streak reset)", url, status)
			consecutive = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll cancelled: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("marker %q not seen on %d consecutive hits within %s", marker, consecutiveTarget, budget)
}

// --------------------------------------------------------------------
// Tests — ordered fast-to-slow. Use `go test -run` to target subsets.
// --------------------------------------------------------------------

func TestHealthZ(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/healthz", "", nil, &resp); err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if !resp.OK {
		t.Fatalf("healthz returned ok=false")
	}
}

func TestWhoAmI(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var resp struct {
		Login           string   `json:"login"`
		AuthorizedSites []string `json:"authorizedSites"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/whoami", c.GHToken, nil, &resp); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if resp.Login == "" {
		t.Fatalf("whoami returned empty login")
	}
	t.Logf("[whoami] login=%s authorizedSites=%v", resp.Login, resp.AuthorizedSites)
	if !slices.Contains(resp.AuthorizedSites, c.Site) {
		t.Fatalf("whoami: site %q not in authorized list %v — caller's GH teams must match the site's teams in the artemis registry (run `universe sites ls | grep %q` to inspect)",
			c.Site, resp.AuthorizedSites, c.Site)
	}
}

func TestAuthRejections(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("bad_token", func(t *testing.T) {
		status, body := c.statusOnly(ctx, http.MethodGet, "/api/whoami",
			"ghp_definitely_not_real_token_for_negative_test_xyz_001", nil)
		if status != 401 && status != 403 {
			t.Fatalf("bad token: status=%d body=%s — want 401|403", status, truncate(body, 200))
		}
	})

	t.Run("missing_token", func(t *testing.T) {
		status, _ := c.statusOnly(ctx, http.MethodGet, "/api/whoami", "", nil)
		if status != 401 {
			t.Fatalf("missing token: status=%d, want 401", status)
		}
	})

	t.Run("unauthorized_site", func(t *testing.T) {
		body := map[string]any{
			"site": "_integration_unknown_site_zzz",
			"sha":  "deadbeef",
		}
		status, _ := c.statusOnly(ctx, http.MethodPost, "/api/deploy/init", c.GHToken, body)
		if status != 403 {
			t.Fatalf("unauthorized site: status=%d, want 403 (site_unauthorized)", status)
		}
	})

	t.Run("missing_site_field", func(t *testing.T) {
		body := map[string]any{"sha": "abc"} // no site
		status, _ := c.statusOnly(ctx, http.MethodPost, "/api/deploy/init", c.GHToken, body)
		if status != 400 {
			t.Fatalf("missing site: status=%d, want 400", status)
		}
	})
}

// TestDeployFlow exercises the full happy-path:
//
//	init → upload → finalize(preview) → curl preview → promote → curl prod → list
//
// Rollback is exercised in a separate test that requires ≥2 prior
// deploys (this one creates one of them).
func TestDeployFlow(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	marker := fmt.Sprintf("artemis-integration-%d", time.Now().UnixNano())
	sha := fmt.Sprintf("it%07d", time.Now().Unix()%10_000_000)
	body := []byte(fmt.Sprintf(
		"<!doctype html><html><body><h1>%s</h1></body></html>\n", marker))

	// 1. init
	t.Logf("[1/7] POST /api/deploy/init site=%s sha=%s", c.Site, sha)
	var initResp struct {
		DeployID  string `json:"deployId"`
		JWT       string `json:"jwt"`
		ExpiresAt string `json:"expiresAt"`
	}
	initReq := map[string]any{
		"site":  c.Site,
		"sha":   sha,
		"files": []string{"index.html"},
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/deploy/init", c.GHToken, initReq, &initResp); err != nil {
		t.Fatalf("init: %v", err)
	}
	if initResp.DeployID == "" || initResp.JWT == "" {
		t.Fatalf("init returned empty deployId or jwt: %+v", initResp)
	}
	t.Logf("       deployId=%s expires=%s", initResp.DeployID, initResp.ExpiresAt)
	registerDeployCleanup(t, initResp.DeployID)

	// 2. upload
	t.Logf("[2/7] PUT  /api/deploy/%s/upload?path=index.html (%d bytes)",
		initResp.DeployID, len(body))
	var upResp struct {
		Received string `json:"received"`
		Key      string `json:"key"`
	}
	if err := c.doUpload(ctx, initResp.DeployID, initResp.JWT,
		"index.html", "text/html; charset=utf-8", body, &upResp); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if upResp.Received != "index.html" {
		t.Fatalf("upload: received=%q want %q", upResp.Received, "index.html")
	}
	t.Logf("       key=%s", upResp.Key)

	// 3. finalize → preview
	t.Logf("[3/7] POST /api/deploy/%s/finalize mode=preview", initResp.DeployID)
	finReq := map[string]any{
		"mode":  "preview",
		"files": []string{"index.html"},
	}
	var finResp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
		Mode     string `json:"mode"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/deploy/%s/finalize", initResp.DeployID),
		initResp.JWT, finReq, &finResp); err != nil {
		t.Fatalf("finalize preview: %v", err)
	}
	t.Logf("       finalize ok url=%s", finResp.URL)

	// 4. curl preview (Caddy r2_alias + CF cache)
	previewURL := fmt.Sprintf("https://%s.preview.%s/", c.Site, c.RootDomain)
	t.Logf("[4/7] GET %s — poll up to %s for marker", previewURL, c.PreviewSLO)
	if err := c.pollForMarker(t, previewURL, marker, c.PreviewSLO); err != nil {
		t.Fatalf("preview: %v", err)
	}

	// 5. promote
	t.Logf("[5/7] POST /api/site/%s/promote", c.Site)
	var promoteResp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/promote", c.Site),
		c.GHToken, nil, &promoteResp); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoteResp.DeployID != initResp.DeployID {
		t.Fatalf("promote: deployId=%q want %q (preview alias drift?)",
			promoteResp.DeployID, initResp.DeployID)
	}
	t.Logf("       promoted to deployId=%s url=%s", promoteResp.DeployID, promoteResp.URL)

	// 6. curl production (≤ 2 min SLO)
	prodURL := fmt.Sprintf("https://%s.%s/", c.Site, c.RootDomain)
	t.Logf("[6/7] GET %s — poll up to %s for marker (prod SLO)", prodURL, c.ProdSLO)
	if err := c.pollForMarker(t, prodURL, marker, c.ProdSLO); err != nil {
		t.Fatalf("production: %v", err)
	}

	// 7. list deploys, verify our deployId surfaces
	t.Logf("[7/7] GET /api/site/%s/deploys", c.Site)
	var deploys []struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/api/site/%s/deploys", c.Site),
		c.GHToken, nil, &deploys); err != nil {
		t.Fatalf("siteDeploys: %v", err)
	}
	found := false
	for _, d := range deploys {
		if d.DeployID == initResp.DeployID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("deploy %s not in /deploys response (got %d entries)",
			initResp.DeployID, len(deploys))
	}
	t.Logf("OK — full deploy flow green for site=%s deployId=%s",
		c.Site, initResp.DeployID)
}

// TestRollback rewires the production alias to a prior deploy id and
// asserts the API echoes the target. Skipped if fewer than 2 deploys
// exist (e.g. on a fresh site).
//
// We do NOT verify served content for the prior deploy — the cleanup
// cron may have purged its prefix (7-day retention). This test covers
// the API contract; full content rollback is exercised by running
// TestDeployFlow twice in a row (CI doubles up).
func TestRollback(t *testing.T) {
	c := loadCfg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var deploys []struct {
		DeployID string `json:"deployId"`
	}
	if err := c.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/api/site/%s/deploys", c.Site),
		c.GHToken, nil, &deploys); err != nil {
		t.Fatalf("siteDeploys: %v", err)
	}
	if len(deploys) < 2 {
		t.Skipf("need ≥2 deploys for rollback test, have %d (run TestDeployFlow first or twice)", len(deploys))
	}
	target := deploys[1].DeployID
	t.Logf("[rollback] target=%s (current head=%s)", target, deploys[0].DeployID)

	var resp struct {
		URL      string `json:"url"`
		DeployID string `json:"deployId"`
	}
	body := map[string]any{"to": target}
	if err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/site/%s/rollback", c.Site),
		c.GHToken, body, &resp); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if resp.DeployID != target {
		t.Fatalf("rollback: deployId=%q want %q", resp.DeployID, target)
	}
	// Suite-level teardown (TestMain) restores prod alias to the
	// baseline deploy captured at setup time. Per-test restore would
	// race with TestMain on parallel runs and obscures intent.
}
