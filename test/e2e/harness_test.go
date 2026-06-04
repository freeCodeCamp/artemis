//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/freeCodeCamp/artemis/internal/r2"
)

type env struct {
	ArtemisURL string
	GHToken    string
	PGDSN      string
	R2Endpoint string
	R2Key      string
	R2Secret   string
	R2Bucket   string
	HTTP       *http.Client
}

var suite env

func TestMain(m *testing.M) {
	suite = env{
		ArtemisURL: strings.TrimRight(os.Getenv("ARTEMIS_URL"), "/"),
		GHToken:    os.Getenv("E2E_GH_TOKEN"),
		PGDSN:      os.Getenv("E2E_PG_DSN"),
		R2Endpoint: os.Getenv("E2E_R2_ENDPOINT"),
		R2Key:      os.Getenv("E2E_R2_ACCESS_KEY_ID"),
		R2Secret:   os.Getenv("E2E_R2_SECRET_ACCESS_KEY"),
		R2Bucket:   os.Getenv("E2E_R2_BUCKET"),
		HTTP:       newHTTPClient(os.Getenv("E2E_R2_CA_FILE")),
	}

	if suite.ArtemisURL == "" {
		log.Printf("[setup] ARTEMIS_URL unset; tests will Skip. Run via: just e2e-local")
		os.Exit(m.Run())
	}

	if err := waitReady(suite); err != nil {
		log.Printf("[setup] FATAL: artemis readyz preflight failed: %v", err)
		os.Exit(2)
	}
	log.Printf("[setup] artemis ready at %s", suite.ArtemisURL)

	os.Exit(m.Run())
}

func newHTTPClient(caFile string) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if caFile == "" {
		return c
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		log.Printf("[setup] WARN: read E2E_R2_CA_FILE %q: %v", caFile, err)
		return c
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		log.Printf("[setup] WARN: no certs parsed from %q", caFile)
		return c
	}
	c.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	return c
}

func requireStack(t *testing.T) env {
	t.Helper()
	if suite.ArtemisURL == "" {
		t.Skip("ARTEMIS_URL unset; run via: just e2e-local")
	}
	return suite
}

func waitReady(e env) error {
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		status, _, err := e.raw(ctx, http.MethodGet, "/readyz", "", nil)
		cancel()
		if err == nil && status == http.StatusOK {
			return nil
		}
		last = err
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("readyz not green within 60s: %v", last)
}

func (e env) r2Client(t *testing.T) *r2.Client {
	t.Helper()
	if e.R2Endpoint == "" {
		t.Skip("E2E_R2_ENDPOINT unset; run via: just e2e-local")
	}
	cli, err := r2.New(context.Background(), r2.Config{
		Endpoint:        e.R2Endpoint,
		AccessKeyID:     e.R2Key,
		SecretAccessKey: e.R2Secret,
		Bucket:          e.R2Bucket,
		Region:          "auto",
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	return cli
}

func (e env) pgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if e.PGDSN == "" {
		t.Skip("E2E_PG_DSN unset; run via: just e2e-local")
	}
	pool, err := pgxpool.New(context.Background(), e.PGDSN)
	if err != nil {
		t.Fatalf("pg pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (e env) raw(ctx context.Context, method, path, bearer string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.ArtemisURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

func (e env) call(t *testing.T, method, path, bearer string, reqBody, respBody any) int {
	t.Helper()
	var body []byte
	if reqBody != nil {
		var err error
		body, err = json.Marshal(reqBody)
		if err != nil {
			t.Fatalf("marshal req: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, raw, err := e.raw(ctx, method, path, bearer, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if respBody != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respBody); err != nil {
			t.Fatalf("%s %s: decode resp (status=%d): %v body=%s", method, path, status, err, truncate(raw, 300))
		}
	}
	return status
}

func (e env) upload(t *testing.T, deployID, jwt, relPath, contentType string, body []byte, respBody any) int {
	t.Helper()
	url := fmt.Sprintf("%s/api/deploy/%s/upload?path=%s", e.ArtemisURL, deployID, relPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("upload req: %v", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.ContentLength = int64(len(body))
	resp, err := e.HTTP.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if respBody != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respBody); err != nil {
			t.Fatalf("upload decode (status=%d): %v body=%s", resp.StatusCode, err, truncate(raw, 300))
		}
	}
	return resp.StatusCode
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

func mustStatus(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status=%d want=%d", what, got, want)
	}
}
