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
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

	// lastPutContentLength captures the most recent value of the
	// `Content-Length` header on a PutObject — used by B18 tests.
	lastPutContentLength int64
	// lastPutTransferEncoding captures the Transfer-Encoding header.
	// Aws-sdk-go-v2 sends "chunked" when ContentLength is unknown.
	lastPutTransferEncoding string

	pageSize           int
	deleteObjectsCalls int
	lastDeleteBatch    int
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
		if r.URL.Query().Has("delete") && r.Method == http.MethodPost {
			f.deleteObjects(w, r)
			return
		}
		http.Error(w, "unsupported bucket op", http.StatusBadRequest)
		return
	}
	key := parts[1]
	switch r.Method {
	case http.MethodPut:
		if src := r.Header.Get("X-Amz-Copy-Source"); src != "" {
			f.copyObject(w, key, src)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.objects[f.bucket+"/"+key] = body
		f.lastPutContentLength = r.ContentLength
		f.lastPutTransferEncoding = ""
		if len(r.TransferEncoding) > 0 {
			f.lastPutTransferEncoding = r.TransferEncoding[0]
		}
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
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	KeyCount              int            `xml:"KeyCount"`
	IsTruncated           bool           `xml:"IsTruncated"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	Contents              []listContent  `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}

type listContent struct {
	Key  string `xml:"Key"`
	Size int    `xml:"Size"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

func (f *fakeS3) listV2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	maxKeys := q.Get("max-keys")
	contToken := q.Get("continuation-token")

	f.mu.Lock()
	f.lastListMaxKeys = maxKeys
	var keys []string
	for k := range f.objects {
		if !strings.HasPrefix(k, f.bucket+"/") {
			continue
		}
		key := strings.TrimPrefix(k, f.bucket+"/")
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		keys = append(keys, key)
	}
	sizes := make(map[string]int, len(keys))
	for _, key := range keys {
		sizes[key] = len(f.objects[f.bucket+"/"+key])
	}
	f.mu.Unlock()
	sort.Strings(keys)

	var contents []listContent
	commonSet := map[string]struct{}{}
	var common []string
	for _, key := range keys {
		if delimiter != "" {
			rest := strings.TrimPrefix(key, prefix)
			if i := strings.Index(rest, delimiter); i >= 0 {
				cp := prefix + rest[:i+len(delimiter)]
				if _, ok := commonSet[cp]; !ok {
					commonSet[cp] = struct{}{}
					common = append(common, cp)
				}
				continue
			}
		}
		contents = append(contents, listContent{Key: key, Size: sizes[key]})
	}
	sort.Strings(common)

	pageSize := f.pageSize
	if maxKeys != "" {
		if n, err := strconv.Atoi(maxKeys); err == nil && n >= 0 {
			pageSize = n
		}
	}
	start := 0
	if contToken != "" {
		start = sort.Search(len(contents), func(i int) bool { return contents[i].Key > contToken })
	}
	end := len(contents)
	if pageSize > 0 && start+pageSize < end {
		end = start + pageSize
	}
	truncated := end < len(contents)
	page := contents[start:end]

	res := listResult{
		Name:        f.bucket,
		Prefix:      prefix,
		KeyCount:    len(page),
		IsTruncated: truncated,
		Contents:    page,
	}
	if truncated && len(page) > 0 {
		res.NextContinuationToken = page[len(page)-1].Key
	}
	for _, cp := range common {
		res.CommonPrefixes = append(res.CommonPrefixes, commonPrefix{Prefix: cp})
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

func (f *fakeS3) copyObject(w http.ResponseWriter, destKey, copySource string) {
	src, err := url.PathUnescape(copySource)
	if err != nil {
		src = copySource
	}
	src = strings.TrimPrefix(src, "/")
	srcKey := strings.TrimPrefix(src, f.bucket+"/")

	f.mu.Lock()
	body, ok := f.objects[f.bucket+"/"+srcKey]
	if ok {
		buf := make([]byte, len(body))
		copy(buf, body)
		f.objects[f.bucket+"/"+destKey] = buf
	}
	f.mu.Unlock()
	if !ok {
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"deadbeef"</ETag></CopyObjectResult>`)
}

func (f *fakeS3) deleteObjects(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		XMLName xml.Name `xml:"Delete"`
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
		Quiet bool `xml:"Quiet"`
	}
	_ = xml.Unmarshal(body, &req)

	f.mu.Lock()
	f.deleteObjectsCalls++
	f.lastDeleteBatch = len(req.Objects)
	var deleted []string
	for _, o := range req.Objects {
		delete(f.objects, f.bucket+"/"+o.Key)
		deleted = append(deleted, o.Key)
	}
	f.mu.Unlock()

	type deletedEntry struct {
		Key string `xml:"Key"`
	}
	var res struct {
		XMLName xml.Name       `xml:"DeleteResult"`
		Deleted []deletedEntry `xml:"Deleted"`
	}
	if !req.Quiet {
		for _, k := range deleted {
			res.Deleted = append(res.Deleted, deletedEntry{Key: k})
		}
	}
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

	body := []byte("<h1>hello</h1>")
	require.NoError(t, c.PutObject(context.Background(),
		"www/deploys/d1/index.html",
		bytes.NewReader(body),
		"text/html",
		int64(len(body))))

	fake.mu.Lock()
	stored := fake.objects[fake.bucket+"/www/deploys/d1/index.html"]
	fake.mu.Unlock()
	assert.Equal(t, "<h1>hello</h1>", string(stored))
}

// TestPutObject_PropagatesContentLength — B18: when caller knows the
// body size up-front (HTTP request with a Content-Length header), the
// upstream R2 PUT must include it. Avoids aws-sdk-go-v2 negotiating
// chunked transfer-encoding on every small upload.
func TestPutObject_PropagatesContentLength(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	body := []byte("twelve-bytes")
	require.NoError(t, c.PutObject(context.Background(),
		"k", bytes.NewReader(body), "application/octet-stream", int64(len(body))))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.EqualValues(t, len(body), fake.lastPutContentLength,
		"expected Content-Length to propagate to R2 PUT")
	assert.NotEqual(t, "chunked", fake.lastPutTransferEncoding,
		"with known length the SDK should NOT use chunked encoding")
}

// TestPutObject_UnknownLengthOk — caller passes 0 (unknown). PUT still
// succeeds; SDK falls back to its default behavior (typically chunked).
func TestPutObject_UnknownLengthOk(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	body := []byte("hello")
	require.NoError(t, c.PutObject(context.Background(),
		"k", bytes.NewReader(body), "text/plain", 0))

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Equal(t, "hello", string(fake.objects[fake.bucket+"/k"]))
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
		require.NoError(t, c.PutObject(context.Background(), key, bytes.NewReader([]byte("x")), "text/plain", 1))
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

// TestNewDeployIDWithClock_Deterministic — B17: NewDeployIDWithClock
// accepts an injectable clock so tests can assert the literal output
// without race-prone wallclock comparisons. Required for any caller
// that wants to verify the encoded timestamp.
func TestNewDeployIDWithClock_Deterministic(t *testing.T) {
	fixed := func() time.Time {
		return time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC)
	}
	id := NewDeployIDWithClock(fixed, "deadbeef0000")
	assert.Equal(t, "20260420-141522-deadbee", id)
}

// TestHasPrefix_TrueWhenObjectsExist — B6: existence probe must return
// true on a prefix that has at least one object, without paginating.
func TestHasPrefix_TrueWhenObjectsExist(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	require.NoError(t, c.PutObject(context.Background(),
		"www/deploys/d1/index.html",
		bytes.NewReader([]byte("x")), "text/plain", 1))

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
			bytes.NewReader([]byte("x")), "text/plain", 1))
	}

	fake.lastListMaxKeys = ""
	ok, err := c.HasPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "1", fake.lastListMaxKeys,
		"HasPrefix must send max-keys=1 to bound R2 cost")
}

func TestDeleteObject_Idempotent(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	require.NoError(t, c.PutObject(context.Background(), "www/x", bytes.NewReader([]byte("y")), "text/plain", 1))

	require.NoError(t, c.DeleteObject(context.Background(), "www/x"))
	fake.mu.Lock()
	_, present := fake.objects["b/www/x"]
	fake.mu.Unlock()
	assert.False(t, present, "object should be gone after delete")

	require.NoError(t, c.DeleteObject(context.Background(), "www/x"), "re-delete is a no-op")
	require.NoError(t, c.DeleteObject(context.Background(), "never-existed"), "delete of missing key is a no-op")
}

func TestDeletePrefix(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	for _, k := range []string{
		"www/deploys/d1/index.html",
		"www/deploys/d1/assets/app.js",
		"www/deploys/d1/style.css",
		"www/deploys/d2/index.html",
	} {
		require.NoError(t, c.PutObject(context.Background(), k, bytes.NewReader([]byte("z")), "text/plain", 1))
	}

	n, err := c.DeletePrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	gone, err := c.HasPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.False(t, gone, "d1 prefix must be empty after DeletePrefix")
	kept, err := c.HasPrefix(context.Background(), "www/deploys/d2/")
	require.NoError(t, err)
	assert.True(t, kept, "sibling prefix d2 must be untouched")
}

func TestDeletePrefix_Paginates(t *testing.T) {
	fake := newFakeS3(t, "b")
	fake.pageSize = 2
	c := newClient(t, fake)
	for i := 0; i < 5; i++ {
		require.NoError(t, c.PutObject(context.Background(),
			fmtKey("s/deploys/d/f%02d.html", i), bytes.NewReader([]byte("z")), "text/plain", 1))
	}

	n, err := c.DeletePrefix(context.Background(), "s/deploys/d/")
	require.NoError(t, err)
	assert.Equal(t, 5, n)

	gone, err := c.HasPrefix(context.Background(), "s/deploys/d/")
	require.NoError(t, err)
	assert.False(t, gone)

	fake.mu.Lock()
	calls := fake.deleteObjectsCalls
	fake.mu.Unlock()
	assert.GreaterOrEqual(t, calls, 3, "5 objects at pageSize 2 must span >=3 delete batches")
}

func TestDeletePrefix_EmptyNoop(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	n, err := c.DeletePrefix(context.Background(), "absent/")
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	fake.mu.Lock()
	calls := fake.deleteObjectsCalls
	fake.mu.Unlock()
	assert.Equal(t, 0, calls, "no objects under prefix -> no delete batch issued")
}

func TestMovePrefix(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	for k, v := range map[string]string{
		"www/deploys/d1/index.html":    "home",
		"www/deploys/d1/assets/app.js": "js",
		"www/deploys/d2/index.html":    "other",
	} {
		require.NoError(t, c.PutObject(context.Background(), k, bytes.NewReader([]byte(v)), "text/plain", int64(len(v))))
	}

	n, err := c.MovePrefix(context.Background(), "www/deploys/d1/", "_trash/www/d1/")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	src, err := c.HasPrefix(context.Background(), "www/deploys/d1/")
	require.NoError(t, err)
	assert.False(t, src, "source prefix must be empty after move")

	got, err := c.GetAlias(context.Background(), "_trash/www/d1/index.html")
	require.NoError(t, err)
	assert.Equal(t, "home", got, "bytes preserved at destination key")

	kept, err := c.HasPrefix(context.Background(), "www/deploys/d2/")
	require.NoError(t, err)
	assert.True(t, kept, "sibling deploy untouched")
}

func TestMovePrefix_EmptyNoop(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	n, err := c.MovePrefix(context.Background(), "absent/", "_trash/absent/")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestListSites(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)
	for _, k := range []string{
		"www/deploys/d1/index.html",
		"www/production",
		"learn/deploys/d2/x",
		"_trash/www/d9/old.html",
		"_artemis_meta.json",
	} {
		require.NoError(t, c.PutObject(context.Background(), k, bytes.NewReader([]byte("x")), "text/plain", 1))
	}

	sites, err := c.ListSites(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"www", "learn"}, sites,
		"top-level prefixes only; _* (e.g. _trash) excluded, bare objects ignored")
}

func TestVerifyDeployComplete_PassFail(t *testing.T) {
	fake := newFakeS3(t, "b")
	c := newClient(t, fake)

	for _, k := range []string{
		"www/deploys/d1/index.html",
		"www/deploys/d1/assets/app.js",
	} {
		require.NoError(t, c.PutObject(context.Background(), k, bytes.NewReader([]byte("x")), "text/plain", 1))
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
