package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/config/configtest"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakeBucket struct {
	bucket string
	mu     sync.Mutex
	keys   []string
	server *httptest.Server
}

func newFakeBucket(t *testing.T, bucket string, keys ...string) *fakeBucket {
	t.Helper()
	f := &fakeBucket{bucket: bucket, keys: keys}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBucket) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keys)
}

func (f *fakeBucket) copy(src, dst string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.keys, src) {
		return false
	}
	if !slices.Contains(f.keys, dst) {
		f.keys = append(f.keys, dst)
	}
	return true
}

func (f *fakeBucket) remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = slices.DeleteFunc(f.keys, func(k string) bool { return k == key })
}

func (f *fakeBucket) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == f.bucket && r.URL.Query().Get("list-type") == "2" {
		f.listV2(w, r.URL.Query().Get("prefix"))
		return
	}
	key := strings.TrimPrefix(path, f.bucket+"/")
	switch {
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		src, _ := url.PathUnescape(strings.TrimPrefix(
			strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "/"), f.bucket+"/"))
		if !f.copy(src, key) {
			break
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<CopyObjectResult><ETag>"e"</ETag><LastModified>2026-01-01T00:00:00.000Z</LastModified></CopyObjectResult>`)
		return
	case r.Method == http.MethodDelete:
		f.remove(key)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+
		`<Error><Code>NoSuchKey</Code><Message>absent</Message></Error>`)
}

func (f *fakeBucket) listV2(w http.ResponseWriter, prefix string) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, `<Name>%s</Name><Prefix>%s</Prefix><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`,
		f.bucket, prefix)
	n := 0
	for _, k := range f.snapshot() {
		if strings.HasPrefix(k, prefix) {
			n++
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>7</Size>`+
				`<LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents>`, k)
		}
	}
	fmt.Fprintf(&b, `<KeyCount>%d</KeyCount></ListBucketResult>`, n)
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

func driftReportEnv(t *testing.T, dsn, endpoint string) {
	t.Helper()
	configtest.Hermetic(t, config.EnvKeys(), map[string]string{
		"DATABASE_URL":         dsn,
		"R2_ENDPOINT":          endpoint,
		"R2_BUCKET":            "artemis-test",
		"R2_ACCESS_KEY_ID":     "k",
		"R2_SECRET_ACCESS_KEY": "s",
		"DEPLOY_PREFIX_FORMAT": "<site>/deploys/<ts>-<sha>/",
		"GH_CLIENT_ID":         "cid",
		"JWT_SIGNING_KEY":      "0123456789abcdef0123456789abcdef",
		"VALKEY_ADDR":          "127.0.0.1:1",
	})
}

func seedDriftFixture(t *testing.T, dsn string, site sitekey.Dirname, deployID string) {
	t.Helper()
	ctx := context.Background()
	db, err := pg.New(ctx, pg.Config{DatabaseURL: dsn})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, pg.Migrate(ctx, db.Pool))

	_, err = db.Pool.Exec(ctx,
		`INSERT INTO sites (slug, teams, created_at, updated_at, created_by)
		 VALUES ($1, $2, now(), now(), 'test') ON CONFLICT (slug) DO NOTHING`,
		site, []string{"staff"})
	require.NoError(t, err)
	require.NoError(t, pg.NewRepo(db).UpsertDeploy(ctx, site, deployID, time.Now().UTC(), 7, true, "active"))
}

func TestRunDriftReport_ReportsNoDriftWhenTheStoresAgree(t *testing.T) {
	dsn, _ := startDeps(t)
	const site, deployID = "www", "20260101-000000-abc1234"
	seedDriftFixture(t, dsn, site, deployID)

	prefix := string(site) + "/deploys/" + deployID + "/"
	bucket := newFakeBucket(t, "artemis-test", prefix+"index.html", prefix+"_artemis_meta.json")
	driftReportEnv(t, dsn, bucket.server.URL)

	var out bytes.Buffer
	require.NoError(t, runDriftReport(context.Background(), &out))

	got := out.String()
	assert.Contains(t, got, "TOTAL reindex=0 tombstone=0 prune=0 aliased-missing=0 orphan-aliases=0 read-failures=0",
		"a site whose bytes and index agree is not drift")
	assert.Contains(t, got, "SWEPT sites=1 r2-objects=2 pg-deploys=1/1",
		"the sweep must show what it actually read, against a count it did not derive from its own site list")
}

func TestRunDriftReport_FindsTheOrphanBytesOfAFailedUpload(t *testing.T) {
	dsn, _ := startDeps(t)
	const site, indexed = "www", "20260101-000000-abc1234"
	seedDriftFixture(t, dsn, site, indexed)

	live := string(site) + "/deploys/" + indexed + "/"
	abandoned := string(site) + "/deploys/20260102-000000-dead999/"
	bucket := newFakeBucket(t, "artemis-test",
		live+"index.html", live+"_artemis_meta.json", abandoned+"index.html")
	driftReportEnv(t, dsn, bucket.server.URL)

	var out bytes.Buffer
	require.NoError(t, runDriftReport(context.Background(), &out))

	got := out.String()
	assert.Contains(t, got, "TOTAL reindex=0 tombstone=1 prune=0 aliased-missing=0 orphan-aliases=0 read-failures=0",
		"an unmarked prefix past grace with no index row is exactly the failed upload reconcile exists to reclaim")
	assert.Contains(t, got, site)
}

func TestRunDriftReport_FailsWhenR2CannotBeRead(t *testing.T) {
	dsn, _ := startDeps(t)
	seedDriftFixture(t, dsn, "www", "20260101-000000-abc1234")
	driftReportEnv(t, dsn, "http://127.0.0.1:1")

	var out bytes.Buffer
	err := runDriftReport(context.Background(), &out)

	require.Error(t, err, "an unreadable bucket is unknown drift, never zero drift")
	assert.Contains(t, out.String(), "READ FAILED")
}

func TestRunDriftReport_RefusesWithoutADatabase(t *testing.T) {
	driftReportEnv(t, "", "http://127.0.0.1:1")

	err := runDriftReport(context.Background(), &bytes.Buffer{})

	require.ErrorContains(t, err, "DATABASE_URL is required")
}

func TestRunDriftReport_RefusesAnUnusableDeployPrefixFormat(t *testing.T) {
	dsn, _ := startDeps(t)
	seedDriftFixture(t, dsn, "www", "20260101-000000-abc1234")
	driftReportEnv(t, dsn, "http://127.0.0.1:1")
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site><ts>-<sha>/")

	err := runDriftReport(context.Background(), &bytes.Buffer{})

	require.ErrorContains(t, err, "DEPLOY_PREFIX_FORMAT",
		"a layout with no site segment would point every sweep at the wrong keyspace; config.Load "+
			"now rejects it at boot, before the gc wiring that used to be the first to notice")
}

func TestRunDriftReport_RefusesAnUnreachableDatabase(t *testing.T) {
	driftReportEnv(t, "postgres://nobody@127.0.0.1:1/none?sslmode=disable", "http://127.0.0.1:1")

	err := runDriftReport(context.Background(), &bytes.Buffer{})

	require.ErrorContains(t, err, "connect postgres")
}

func TestMain_DriftReportSubcommandExitsNonZeroOnFailure(t *testing.T) {
	if os.Getenv("ARTEMIS_DRIFT_SUBPROCESS") == "1" {
		os.Args = []string{os.Args[0], driftReportCommand}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMain_DriftReportSubcommandExitsNonZeroOnFailure")
	cmd.Env = append(os.Environ(),
		"ARTEMIS_DRIFT_SUBPROCESS=1",
		"DATABASE_URL=",
		"SENTRY_DSN=",
		"VALKEY_ADDR=127.0.0.1:1",
		"R2_ENDPOINT=http://127.0.0.1:1",
		"R2_ACCESS_KEY_ID=k",
		"R2_SECRET_ACCESS_KEY=s",
		"GH_CLIENT_ID=cid",
		"JWT_SIGNING_KEY=0123456789abcdef0123456789abcdef",
	)
	out, err := cmd.CombinedOutput()

	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "a drift report that cannot run must not exit 0")
	require.Equal(t, 1, ee.ExitCode())
	require.Contains(t, string(out), "drift report failed:",
		"the failure names itself on stderr instead of the misleading boot.fatal path")
}
