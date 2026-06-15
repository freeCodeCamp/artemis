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
	"net/url"
	"strings"
	"time"

	awsv2 "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
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
//
// contentLength: pass the body size in bytes when known (Content-Length
// from the originating HTTP request, len() of an in-memory buffer).
// Pass 0 when unknown — the SDK negotiates chunked transfer-encoding
// in that case. Setting ContentLength when known skips that round
// trip and lets R2 short-circuit on small uploads.
func (c *Client) PutObject(ctx context.Context, key string, body io.Reader, contentType string, contentLength int64) error {
	in := &s3.PutObjectInput{
		Bucket: awsv2.String(c.bucket),
		Key:    awsv2.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = awsv2.String(contentType)
	}
	if contentLength > 0 {
		in.ContentLength = awsv2.Int64(contentLength)
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
	return c.PutObject(ctx, aliasKey, strings.NewReader(deployID), "text/plain", int64(len(deployID)))
}

// GetAlias returns the body of the alias key — i.e. the deploy id it
// currently points at. Returns ErrNotFound if the alias hasn't been
// written yet.
//
// aws-sdk-go-v2 surfaces R2's 404 as a typed APIError with code
// NoSuchKey or NotFound; we map both to ErrNotFound. Pre-B24 a
// belt-and-suspenders string fallback (`strings.Contains(err.Error(),
// "404")`) was kept against a hypothetical SDK quirk. With nothing
// deployed yet, removing the fallback simplifies the call site;
// re-evaluate at the next aws-sdk-go-v2 minor bump if needed.
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

func (c *Client) PrefixBytes(ctx context.Context, prefix string) (int64, error) {
	var total int64
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awsv2.String(c.bucket),
			Prefix:            awsv2.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return 0, fmt.Errorf("r2 prefix-bytes %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Size != nil {
				total += *obj.Size
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return total, nil
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: awsv2.String(c.bucket),
		Key:    awsv2.String(key),
	})
	if err != nil {
		return fmt.Errorf("r2 delete %s: %w", key, err)
	}
	return nil
}

const deleteBatchMax = 1000

func (c *Client) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	var deleted int
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awsv2.String(c.bucket),
			Prefix:            awsv2.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return deleted, fmt.Errorf("r2 deleteprefix list %s: %w", prefix, err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(page.Contents))
		for _, obj := range page.Contents {
			if obj.Key != nil {
				ids = append(ids, s3types.ObjectIdentifier{Key: obj.Key})
			}
		}
		for start := 0; start < len(ids); start += deleteBatchMax {
			end := min(start+deleteBatchMax, len(ids))
			n, err := c.deleteBatch(ctx, ids[start:end])
			deleted += n
			if err != nil {
				return deleted, err
			}
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return deleted, nil
}

func encodeCopySource(bucket, key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return bucket + "/" + strings.Join(segs, "/")
}

func (c *Client) deleteBatch(ctx context.Context, ids []s3types.ObjectIdentifier) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	out, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: awsv2.String(c.bucket),
		Delete: &s3types.Delete{Objects: ids, Quiet: awsv2.Bool(true)},
	})
	if err != nil {
		return 0, fmt.Errorf("r2 deleteobjects: %w", err)
	}
	if len(out.Errors) > 0 {
		key, msg := "", ""
		if out.Errors[0].Key != nil {
			key = *out.Errors[0].Key
		}
		if out.Errors[0].Message != nil {
			msg = *out.Errors[0].Message
		}
		return len(ids) - len(out.Errors), fmt.Errorf("r2 deleteobjects: %d of %d failed (first %s: %s)", len(out.Errors), len(ids), key, msg)
	}
	return len(ids), nil
}

func (c *Client) MovePrefix(ctx context.Context, srcPrefix, dstPrefix string) (int, error) {
	var moved int
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awsv2.String(c.bucket),
			Prefix:            awsv2.String(srcPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return moved, fmt.Errorf("r2 moveprefix list %s: %w", srcPrefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			key := *obj.Key
			dstKey := dstPrefix + strings.TrimPrefix(key, srcPrefix)
			_, err := c.s3.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     awsv2.String(c.bucket),
				Key:        awsv2.String(dstKey),
				CopySource: awsv2.String(encodeCopySource(c.bucket, key)),
			})
			if err != nil {
				return moved, fmt.Errorf("r2 moveprefix copy %s->%s: %w", key, dstKey, err)
			}
			if err := c.DeleteObject(ctx, key); err != nil {
				return moved, fmt.Errorf("r2 moveprefix delete %s: %w", key, err)
			}
			moved++
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return moved, nil
}

func (c *Client) ListSites(ctx context.Context) ([]string, error) {
	var sites []string
	var token *string
	for {
		page, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            awsv2.String(c.bucket),
			Delimiter:         awsv2.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("r2 listsites: %w", err)
		}
		for _, cp := range page.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			site := strings.TrimSuffix(*cp.Prefix, "/")
			if site == "" || strings.HasPrefix(site, "_") {
				continue
			}
			sites = append(sites, site)
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		token = page.NextContinuationToken
	}
	return sites, nil
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

// NewDeployID builds a deploy id of the form <yyyymmdd-hhmmss>-<sha7>
// using time.Now() as the clock source.
//
// For deterministic output (tests, replays) use NewDeployIDWithClock.
func NewDeployID(commitSHA string) string {
	return NewDeployIDWithClock(time.Now, commitSHA)
}

// NewDeployIDWithClock is NewDeployID with an injectable clock. Pass
// time.Now in production; pass a fixed-time func in tests.
func NewDeployIDWithClock(now func() time.Time, commitSHA string) string {
	short := commitSHA
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s-%s", now().UTC().Format("20060102-150405"), short)
}

// ErrNotFound is returned by GetAlias when the alias key doesn't exist.
var ErrNotFound = errors.New("r2: not found")

// IsNotFound reports whether err is an ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
