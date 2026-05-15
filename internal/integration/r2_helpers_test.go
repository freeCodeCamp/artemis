//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
)

const r2SkipUsage = `

R2 direct-read probe skipped: %s not set.

To enable byte-level alias assertions (TestAliasBodyRoundTrip,
TestDeployPromoteSkipsPreview), export an R2 read-only key scoped to
the configured bucket, alongside the artemis env:

  R2_ENDPOINT='https://<account>.r2.cloudflarestorage.com'
  R2_ACCESS_KEY_ID='<r2 read-only access key id>'
  R2_SECRET_ACCESS_KEY='<r2 read-only secret access key>'
  R2_BUCKET='<bucket-name>'                                    # optional

Tests without these vars still run; R2-direct probes Skip with a
clear log.

Alias key format defaults must match the artemis deployment under
test. Override via env when the deployment uses non-default formats:
  ALIAS_PRODUCTION_KEY_FORMAT='<site>.<root>/production'
  ALIAS_PREVIEW_KEY_FORMAT='<site>.<root>/preview'
`

// errAliasNotFound is returned by r2Probe.getAlias / .headObject when
// the alias key does not exist. Callers distinguish via errors.Is.
var errAliasNotFound = errors.New("r2 probe: alias key not found")

// deployIDPattern matches artemis-minted deploy IDs (NewDeployID in
// internal/r2/r2.go:218-233): "<yyyymmdd-hhmmss>-<sha-or-synthetic>".
// The third segment is whatever the caller passed for SHA — git
// hashes (hex) in normal flow, but universe-cli emits `nogit-<base36>`
// when run outside a git tree (deploy.ts:91-93), and integration
// tests use bespoke prefixes (`sp86054`, `rA86019`). Accept any
// non-whitespace suffix; tighter validation belongs in artemis
// itself, not in the test probe.
var deployIDPattern = regexp.MustCompile(`^\d{8}-\d{6}-\S+$`)

// r2Probe is the narrowed R2 read surface used by integration tests.
// Mirrors the SDK + path-style + region=auto setup the production
// artemis code uses in internal/r2/r2.go:46-71 so the probe sees the
// same bucket bytes artemis writes.
type r2Probe struct {
	s3     *s3.Client
	bucket string
}

// r2Client returns a configured R2 read probe or t.Skip()s if any of
// R2_ENDPOINT/R2_ACCESS_KEY_ID/R2_SECRET_ACCESS_KEY are unset.
//
// The credentials are an R2 read-only key scoped to the configured
// bucket; provisioning is operator-specific.
func r2Client(t *testing.T) r2Probe {
	t.Helper()
	endpoint := os.Getenv("R2_ENDPOINT")
	keyID := os.Getenv("R2_ACCESS_KEY_ID")
	secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	if endpoint == "" {
		t.Skipf(r2SkipUsage, "R2_ENDPOINT")
	}
	if keyID == "" {
		t.Skipf(r2SkipUsage, "R2_ACCESS_KEY_ID")
	}
	if secret == "" {
		t.Skipf(r2SkipUsage, "R2_SECRET_ACCESS_KEY")
	}
	bucket := envDefault("R2_BUCKET", "universe-static-apps-01")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("auto"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(keyID, secret, "")),
	)
	if err != nil {
		t.Fatalf("r2: load aws config: %v", err)
	}
	cli := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = awsv2.String(endpoint)
		o.UsePathStyle = true
	})
	return r2Probe{s3: cli, bucket: bucket}
}

// aliasKey returns the R2 key for the named alias mode. Format defaults
// to the production artemis configuration:
//
//	preview     → <site>.freecode.camp/preview
//	production  → <site>.freecode.camp/production
//
// Override via ALIAS_PRODUCTION_KEY_FORMAT / ALIAS_PREVIEW_KEY_FORMAT
// (same env names artemis itself uses; see internal/config/config.go:162-167)
// when testing a non-prod artemis with a different layout.
func aliasKey(c cfg, mode string) string {
	var raw string
	switch mode {
	case "production":
		raw = envDefault("ALIAS_PRODUCTION_KEY_FORMAT",
			fmt.Sprintf("<site>.%s/production", c.RootDomain))
	default:
		raw = envDefault("ALIAS_PREVIEW_KEY_FORMAT",
			fmt.Sprintf("<site>.%s/preview", c.RootDomain))
	}
	return strings.ReplaceAll(raw, "<site>", c.Site)
}

// getAlias returns the body of the named R2 key as a string. Returns
// errAliasNotFound when the key does not exist; other errors are
// wrapped with the key for diagnostic context.
func (r r2Probe) getAlias(ctx context.Context, key string) (string, error) {
	out, err := r.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awsv2.String(r.bucket),
		Key:    awsv2.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NoSuchKey", "NotFound":
				return "", errAliasNotFound
			}
		}
		return "", fmt.Errorf("r2 get %s: %w", key, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("r2 read %s: %w", key, err)
	}
	return string(body), nil
}

// headObject reports whether key exists. Returns nil for 2xx, an error
// otherwise. Wraps NotFound as errAliasNotFound so callers can branch
// via errors.Is.
func (r r2Probe) headObject(ctx context.Context, key string) error {
	_, err := r.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: awsv2.String(r.bucket),
		Key:    awsv2.String(key),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NoSuchKey", "NotFound":
				return errAliasNotFound
			}
		}
		return fmt.Errorf("r2 head %s: %w", key, err)
	}
	return nil
}

// TestR2ProbeWiring is the wiring proof for the T1 helper: confirms
// r2Client + aliasKey + getAlias actually talk to R2. Reads the current
// production alias body for SITE and asserts it parses as a deploy id.
// Skips cleanly if R2_* env unset (per r2Client semantics) so
// `make integration` regression-free without R2 creds.
func TestR2ProbeWiring(t *testing.T) {
	c := loadCfg(t)
	r := r2Client(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key := aliasKey(c, "production")
	body, err := r.getAlias(ctx, key)
	if err != nil {
		if errors.Is(err, errAliasNotFound) {
			t.Skipf("production alias %q not yet written for site=%s — fresh site? Run TestDeployFlow first.",
				key, c.Site)
		}
		t.Fatalf("getAlias %s: %v", key, err)
	}
	trimmed := strings.TrimSpace(body)
	if !deployIDPattern.MatchString(trimmed) {
		t.Fatalf("alias %s body=%q does not match deploy-id pattern", key, trimmed)
	}
	t.Logf("[r2-probe] %s body=%q (wiring green)", key, trimmed)
}
