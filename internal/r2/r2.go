// Package r2 wraps the AWS SDK Go v2 S3 client for Cloudflare R2 with the
// narrow operations Artemis needs:
//
//   - PutObject  — stream a file into <site>/deploys/<id>/<path>
//   - PutAlias   — atomic single-PUT to <site>/preview or <site>/production
//   - GetAlias   — read alias body (the deploy id pointer)
//   - ListPrefix — enumerate keys under a prefix (used by VerifyDeployComplete)
//   - VerifyDeployComplete — list-then-compare expected files
//
// R2 is S3-compatible. Region is set to "auto" per Cloudflare guidance.
package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
)

// Config carries the R2 (S3-compatible) target.
type Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	Region          string // default "auto"
}

// Client is the narrowed wrapper over s3.Client used by Artemis.
type Client struct {
	s3     *s3.Client
	bucket string
}

// New returns a configured client. Uses path-style addressing because R2
// doesn't support virtual-hosted bucket URLs the way AWS does, and the
// httptest stand-in used in tests speaks path-style natively.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("r2: bucket is required")
	}
	if cfg.Endpoint == "" {
		return nil, errors.New("r2: endpoint is required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("r2: load aws config: %w", err)
	}

	cli := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = awsv2.String(cfg.Endpoint)
		o.UsePathStyle = true
	})

	return &Client{s3: cli, bucket: cfg.Bucket}, nil
}

// PutObject streams body into <bucket>/<key>.
func (c *Client) PutObject(ctx context.Context, key string, body io.Reader, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket: awsv2.String(c.bucket),
		Key:    awsv2.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = awsv2.String(contentType)
	}
	_, err := c.s3.PutObject(ctx, in)
	if err != nil {
		return fmt.Errorf("r2 put %s: %w", key, err)
	}
	return nil
}

// PutAlias writes a small text body (the deploy id pointer) to the alias
// key. Single PUT is atomic per-key in R2.
func (c *Client) PutAlias(ctx context.Context, aliasKey, deployID string) error {
	return c.PutObject(ctx, aliasKey, strings.NewReader(deployID), "text/plain")
}

// GetAlias returns the body of the alias key — i.e. the deploy id it
// currently points at. Returns ErrNotFound if the alias hasn't been
// written yet.
func (c *Client) GetAlias(ctx context.Context, aliasKey string) (string, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: awsv2.String(c.bucket),
		Key:    awsv2.String(aliasKey),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NoSuchKey", "NotFound":
				return "", ErrNotFound
			}
		}
		// Fallback: look at status hints embedded in the error string.
		if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("r2 get %s: %w", aliasKey, err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return "", fmt.Errorf("r2 read %s: %w", aliasKey, err)
	}
	return string(body), nil
}

// HasPrefix reports whether at least one object exists under prefix.
// Implementation: a single ListObjectsV2 with MaxKeys=1, no pagination.
// Bounds R2 cost on existence probes (used by SiteRollback) regardless
// of how many objects share the prefix.
func (c *Client) HasPrefix(ctx context.Context, prefix string) (bool, error) {
	page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  awsv2.String(c.bucket),
		Prefix:  awsv2.String(prefix),
		MaxKeys: awsv2.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("r2 hasprefix %s: %w", prefix, err)
	}
	return len(page.Contents) > 0, nil
}

// ListPrefix returns all keys under the given prefix.
func (c *Client) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awsv2.String(c.bucket),
			Prefix:            awsv2.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("r2 list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				out = append(out, *obj.Key)
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return out, nil
}

// VerifyError is returned when VerifyDeployComplete finds expected files
// missing from the deploy prefix. The Missing field lists the files that
// did not surface in the listing.
type VerifyError struct {
	Prefix  string
	Missing []string
}

func (e *VerifyError) Error() string {
	return fmt.Sprintf("r2 verify: prefix %q missing %d file(s): %s",
		e.Prefix, len(e.Missing), strings.Join(e.Missing, ", "))
}

// VerifyDeployComplete asserts that every relative path in `expected`
// surfaces under prefix when listed via ListObjectsV2. Returns a
// *VerifyError with the missing names on failure.
func (c *Client) VerifyDeployComplete(ctx context.Context, prefix string, expected []string) error {
	keys, err := c.ListPrefix(ctx, prefix)
	if err != nil {
		return err
	}
	have := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		have[strings.TrimPrefix(k, prefix)] = struct{}{}
	}
	var missing []string
	for _, want := range expected {
		if _, ok := have[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return &VerifyError{Prefix: prefix, Missing: missing}
	}
	return nil
}

// NewDeployID builds a deploy id of the form <yyyymmdd-hhmmss>-<sha7>.
// The short sha is the first 7 characters of the input commit sha.
func NewDeployID(commitSHA string) string {
	now := time.Now().UTC()
	short := commitSHA
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), short)
}

// ErrNotFound is returned by GetAlias when the alias key doesn't exist.
var ErrNotFound = errors.New("r2: not found")

// IsNotFound reports whether err is an ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
