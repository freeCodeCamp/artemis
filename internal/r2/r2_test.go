package r2

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fmtKey(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// fakeS3 is a minimal in-memory S3-compatible HTTP backend that the AWS
// SDK can talk to via endpoint override + path-style addressing.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key = "<bucket>/<key>"
	server  *httptest.Server
	bucket  string

	// lastListMaxKeys captures the most recent value of the `max-keys`
	// query param on a ListObjectsV2 request — used by HasPrefix tests
	// to assert R2 cost bound.
	lastListMaxKeys string
}

func newFakeS3(t *testing.T, bucket string) *fakeS3 {
	t.Helper()
	f := &fakeS3{
		objects: make(map[string][]byte),
		bucket:  bucket,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handle)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// handle dispatches PUT/GET/HEAD/DELETE/ListObjectsV2.
func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	// Path-style: /<bucket>/<key>
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 0 || parts[0] != f.bucket {
		http.Error(w, "wrong bucket", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		// /<bucket>?list-type=2&prefix=...
		if r.URL.Query().Get("list-type") == "2" {
			f.listV2(w, r)
			return
		}
		http.Error(w, "unsupported bucket op", http.StatusBadRequest)
		return
	}
	key := parts[1]
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.objects[f.bucket+"/"+key] = body
		f.mu.Unlock()
		w.Header().Set("ETag", `"deadbeef"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[f.bucket+"/"+key]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, f.bucket+"/"+key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

type listResult struct {
	XMLName  xml.Name      `xml:"ListBucketResult"`
	Name     string        `xml:"Name"`
	Prefix   string        `xml:"Prefix"`
	KeyCount int           `xml:"KeyCount"`
	Contents []listContent `xml:"Contents"`
}

type listContent struct {
	Key  string `xml:"Key"`
	Size int    `xml:"Size"`
}

func (f *fakeS3) listV2(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	maxKeys := r.URL.Query().Get("max-keys")
	f.mu.Lock()
	f.lastListMaxKeys = maxKeys
	var contents []listContent
	for k, v := range f.objects {
		if !strings.HasPrefix(k, f.bucket+"/") {
			continue
		}
		key := strings.TrimPrefix(k, f.bucket+"/")
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		contents = append(contents, listContent{Key: key, Size: len(v)})
	}
	f.mu.Unlock()

	// Honor max-keys param (string-typed in S3 wire format).
	if maxKeys != "" {
		if n, err := strconv.Atoi(maxKeys); err == nil && n >= 0 && n < len(contents) {
			contents = contents[:n]
		}
	}

	res := listResult{Name: f.bucket, Prefix: prefix, KeyCount: len(contents), Contents: contents}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func newClient(t *testing.T, fake *fakeS3) *Client {
	t.Helper()
	u, err := url.Parse(fake.server.URL)
	require.NoError(t, err)
	c, err := New(context.Background(), Config{
		Endpoint:        u.String(),
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		Bucket:          fake.bucket,
		Region:          "auto",
	})
	require.NoError(t, err)
	return c
}

func TestPutObject_StoresBytes(t *testing.T) {
	fake := newFakeS3(t, "universe-static-apps-01")
	c := newClient(t, fake)

	require.NoError(t, c.PutObject(context.Background(),
		"www/deploys/d1/index.html",
		bytes.NewReader([]byte("<h1>hello</h1>")),
		"text/html"))

	fake.mu.Lock()
	body := fake.objects[fake.bucket+"/www/deploys/d1/index.html"]
	fake.mu.Unlock()
	assert.Equal(t, "<h1>hello</h1>", string(body))
}

func TestPutAlias_AtomicSinglePut(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	require.NoError(t, c.PutAlias(context.Background(), "www/preview", "deploys/20260420-141522-abc1234"))

	fake.mu.Lock()
	body := fake.objects["b/www/preview"]
	fake.mu.Unlock()
	assert.Equal(t, "deploys/20260420-141522-abc1234", string(body))
}

func TestListPrefix_ReturnsKeysUnderPrefix(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	for _, key := range []string{
		"www/deploys/d1/index.html",
		"www/deploys/d1/assets/app.js",
		"www/deploys/d2/index.html",
	} {
		require.NoError(t, c.PutObject(context.Background(), key, bytes.NewReader([]byte("x")), "text/plain"))
	}

	keys, err := c.ListPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{
		"www/deploys/d1/index.html",
		"www/deploys/d1/assets/app.js",
	}, keys)
}

func TestGetAlias_ReturnsBody(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	require.NoError(t, c.PutAlias(context.Background(), "www/production", "deploys/d1"))

	got, err := c.GetAlias(context.Background(), "www/production")
	require.NoError(t, err)
	assert.Equal(t, "deploys/d1", got)
}

func TestGetAlias_NotFound(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	_, err := c.GetAlias(context.Background(), "www/production")
	require.Error(t, err)
	assert.True(t, IsNotFound(err))
}

func TestDeployIDFormat_TimestampPlusShortSha(t *testing.T) {
	id := NewDeployID("abc1234567890")
	// Format: <yyyymmdd-hhmmss>-<sha7>
	assert.Regexp(t, `^\d{8}-\d{6}-abc1234$`, id)
}

// TestHasPrefix_TrueWhenObjectsExist — B6: existence probe must return
// true on a prefix that has at least one object, without paginating.
func TestHasPrefix_TrueWhenObjectsExist(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	require.NoError(t, c.PutObject(context.Background(),
		"www/deploys/d1/index.html",
		bytes.NewReader([]byte("x")), "text/plain"))

	ok, err := c.HasPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestHasPrefix_FalseWhenEmpty — empty prefix returns false, no error.
// This is the path SiteRollback uses to refuse rolling back to a swept
// deploy.
func TestHasPrefix_FalseWhenEmpty(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	ok, err := c.HasPrefix(context.Background(), "www/deploys/gone/")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestHasPrefix_RequestsMaxKeysOne — the whole point of HasPrefix vs
// ListPrefix is to bound the R2 cost: regardless of how many objects
// live under the prefix, we send max-keys=1 and ask for one page.
func TestHasPrefix_RequestsMaxKeysOne(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	for i := 0; i < 5; i++ {
		require.NoError(t, c.PutObject(context.Background(),
			fmtKey("www/deploys/d1/file%02d.html", i),
			bytes.NewReader([]byte("x")), "text/plain"))
	}

	fake.lastListMaxKeys = ""
	ok, err := c.HasPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "1", fake.lastListMaxKeys,
		"HasPrefix must send max-keys=1 to bound R2 cost")
}

func TestVerifyDeployComplete_PassFail(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	for _, k := range []string{
		"www/deploys/d1/index.html",
		"www/deploys/d1/assets/app.js",
	} {
		require.NoError(t, c.PutObject(context.Background(), k, bytes.NewReader([]byte("x")), "text/plain"))
	}

	require.NoError(t, c.VerifyDeployComplete(context.Background(),
		path.Clean("www/deploys/d1/")+"/", []string{"index.html", "assets/app.js"}))

	err := c.VerifyDeployComplete(context.Background(),
		path.Clean("www/deploys/d1/")+"/", []string{"index.html", "assets/app.js", "missing.html"})
	require.Error(t, err)
	var verr *VerifyError
	assert.True(t, errors.As(err, &verr))
	assert.Contains(t, verr.Missing, "missing.html")
}
