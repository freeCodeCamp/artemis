package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func fqdnTemplate(t *testing.T) handler.DeployPrefixTemplate {
	t.Helper()
	tmpl, err := handler.NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	return tmpl
}

func TestResolveSweepSite_AcceptsTheStorageDirnameAsGiven(t *testing.T) {
	known := []sitekey.Dirname{"www.freecode.camp", "test.freecode.camp"}

	got, err := resolveSweepSite(known, fqdnTemplate(t), "test.freecode.camp")

	require.NoError(t, err)
	assert.Equal(t, sitekey.Dirname("test.freecode.camp"), got)
}

func TestResolveSweepSite_MapsARegistrySlugToItsStorageDirname(t *testing.T) {
	known := []sitekey.Dirname{"www.freecode.camp", "test.freecode.camp"}

	got, err := resolveSweepSite(known, fqdnTemplate(t), "test")

	require.NoError(t, err)
	assert.Equal(t, sitekey.Dirname("test.freecode.camp"), got,
		"the registry slug and the storage dirname are different keyspaces, and confusing them is the "+
			"bug that made the reconciler a no-op for months")
}

func TestResolveSweepSite_RefusesANameNeitherKeyspaceKnows(t *testing.T) {
	known := []sitekey.Dirname{"www.freecode.camp"}

	_, err := resolveSweepSite(known, fqdnTemplate(t), "nope")

	require.Error(t, err, "repairing a prefix that no store knows about can only destroy bytes or do nothing")
	assert.Contains(t, err.Error(), "nope")
	assert.Contains(t, err.Error(), "nope.freecode.camp",
		"the error must name both forms it tried, so the operator can see which keyspace was searched")
}

func TestResolveSweepSite_RefusesAnEmptyName(t *testing.T) {
	_, err := resolveSweepSite([]sitekey.Dirname{"www.freecode.camp"}, fqdnTemplate(t), "")

	require.Error(t, err)
}

func TestRunReconcileCLI_RequiresExactlyOneSite(t *testing.T) {
	err := runReconcileCLI(context.Background(), &bytes.Buffer{}, nil)

	require.ErrorContains(t, err, "site",
		"a fleet-wide repair has no safe default; the operator names one site or nothing happens")
}

func TestRunReconcileCLI_RefusesMoreThanOneSite(t *testing.T) {
	err := runReconcileCLI(context.Background(), &bytes.Buffer{}, []string{"a", "b"})

	require.Error(t, err)
}

func TestParseReconcileArgs_DefaultsToDryRun(t *testing.T) {
	site, apply, err := parseReconcileArgs([]string{"www"})

	require.NoError(t, err)
	assert.Equal(t, "www", site)
	assert.False(t, apply, "a repair must be asked for explicitly, never assumed")
}

func TestParseReconcileArgs_ApplyFlagArmsTheRepair(t *testing.T) {
	site, apply, err := parseReconcileArgs([]string{"www", "--apply"})

	require.NoError(t, err)
	assert.Equal(t, "www", site)
	assert.True(t, apply)
}

func TestParseReconcileArgs_ApplyBeforeTheSiteStillParses(t *testing.T) {
	site, apply, err := parseReconcileArgs([]string{"--apply", "www"})

	require.NoError(t, err)
	assert.Equal(t, "www", site)
	assert.True(t, apply)
}

func TestSweepStats_AScopedSweepSkipsTheFleetSelfCheck(t *testing.T) {
	s := sweepStats{Scoped: true, Sites: 1, IndexedTotal: 198, PGDeploys: 0, R2Objects: 0}

	require.NoError(t, s.validate(),
		"one named site with no deploys is an ordinary answer; only a fleet sweep can conclude "+
			"from an empty match that its site list is wrong")
}

func TestSweepStats_AFleetSweepStillCatchesTheKeyspaceBug(t *testing.T) {
	s := sweepStats{Sites: 76, IndexedTotal: 198, PGDeploys: 0, R2Objects: 0}

	require.ErrorContains(t, s.validate(), "site list is wrong",
		"the scoped escape hatch must not disarm the check that catches the original bug")
}

func TestParseReconcileArgs_RejectsAnUnknownFlag(t *testing.T) {
	_, _, err := parseReconcileArgs([]string{"www", "--force"})

	require.Error(t, err, "an unknown flag must never be read as a site name")
}

func TestRunReconcileCLI_DryRunReportsOneSiteAndWritesNothing(t *testing.T) {
	dsn, _ := startDeps(t)
	const site, indexed = "www", "20260101-000000-abc1234"
	seedDriftFixture(t, dsn, site, indexed)

	live := string(site) + "/deploys/" + indexed + "/"
	abandoned := string(site) + "/deploys/20260102-000000-dead999/"
	bucket := newFakeBucket(t, "artemis-test",
		live+"index.html", live+"_artemis_meta.json", abandoned+"index.html")
	driftReportEnv(t, dsn, bucket.server.URL)

	var out bytes.Buffer
	require.NoError(t, runReconcileCLI(context.Background(), &out, []string{site}))

	got := out.String()
	assert.Contains(t, got, "dry run: pass --apply to repair",
		"the default must say plainly that it changed nothing")
	assert.Contains(t, got, "TOTAL reindex=0 tombstone=1 prune=0 aliased-missing=0 read-failures=0")
	assert.Contains(t, got, "SWEPT sites=1",
		"a named-site repair looks at that site alone, never the fleet")
}

func TestRunReconcileCLI_ApplyTrashesTheOrphanAndRecordsItsTombstone(t *testing.T) {
	dsn, _ := startDeps(t)
	const site, indexed = "www", "20260101-000000-abc1234"
	const orphan = "20260102-000000-dead999"
	seedDriftFixture(t, dsn, site, indexed)

	live := string(site) + "/deploys/" + indexed + "/"
	abandoned := string(site) + "/deploys/" + orphan + "/"
	bucket := newFakeBucket(t, "artemis-test",
		live+"index.html", live+"_artemis_meta.json", abandoned+"index.html")
	driftReportEnv(t, dsn, bucket.server.URL)

	var out bytes.Buffer
	require.NoError(t, runReconcileCLI(context.Background(), &out, []string{site, "--apply"}))

	assert.Contains(t, out.String(), "repaired www: reindex=0 tombstone=1 prune=0 aliased-missing=0")

	keys := bucket.snapshot()
	assert.NotContains(t, keys, abandoned+"index.html",
		"--apply is the destructive half: the orphan bytes must actually leave the live prefix")
	assert.Contains(t, keys, "_trash/"+string(site)+"/"+orphan+"/index.html",
		"the bytes move to trash rather than vanish, so a wrong call stays recoverable for the grace window")
	assert.Contains(t, keys, live+"index.html",
		"the indexed deploy is not drift and must survive a repair of its neighbour")

	ctx := context.Background()
	db, err := pg.New(ctx, pg.Config{DatabaseURL: dsn})
	require.NoError(t, err)
	defer db.Close()
	stones, err := pg.NewRepo(db).TombstonesForSite(ctx, site)
	require.NoError(t, err)
	require.Len(t, stones, 1,
		"purge is row-driven, so bytes moved without a tombstone row are a permanent leak")
	assert.Equal(t, orphan, stones[0].ID)
}

func TestMain_ReconcileSubcommandPassesTheSiteThroughAndExitsNonZero(t *testing.T) {
	if os.Getenv("ARTEMIS_RECONCILE_SUBPROCESS") == "1" {
		os.Args = []string{os.Args[0], reconcileCommand, "some-site"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMain_ReconcileSubcommandPassesTheSiteThroughAndExitsNonZero")
	cmd.Env = append(os.Environ(),
		"ARTEMIS_RECONCILE_SUBPROCESS=1",
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
	require.ErrorAs(t, err, &ee, "a reconcile that cannot run must not exit 0")
	require.Equal(t, 1, ee.ExitCode())
	require.Contains(t, string(out), "DATABASE_URL is required",
		"the site name must reach argument parsing; a mis-sliced os.Args would fail on the site count instead")
}

func TestRunReconcileCLI_RefusesASiteNoStoreKnows(t *testing.T) {
	dsn, _ := startDeps(t)
	seedDriftFixture(t, dsn, "www", "20260101-000000-abc1234")
	bucket := newFakeBucket(t, "artemis-test")
	driftReportEnv(t, dsn, bucket.server.URL)

	err := runReconcileCLI(context.Background(), &bytes.Buffer{}, []string{"ghost", "--apply"})

	require.ErrorContains(t, err, "refusing to touch a prefix no store has heard of",
		"an --apply against an unknown name must stop before it reaches R2")
}

func TestRunReconcileCLI_RefusesWithoutADatabase(t *testing.T) {
	driftReportEnv(t, "", "http://127.0.0.1:1")

	err := runReconcileCLI(context.Background(), &bytes.Buffer{}, []string{"www"})

	require.ErrorContains(t, err, "DATABASE_URL is required")
}
