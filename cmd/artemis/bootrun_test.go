package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/config/configtest"
	"github.com/freeCodeCamp/artemis/internal/pg"
	vkstore "github.com/freeCodeCamp/artemis/internal/registry/valkey"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func bootCfg(t *testing.T, dsn, valkeyAddr string, port int) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Port:               port,
		DatabaseURL:        dsn,
		DeployPrefixFormat: "<site>.example.test/deploys/<ts>-<sha>/",
		UploadMaxBytes:     1024,
		LogLevel:           "error",
	}
	cfg.R2.Endpoint = "http://127.0.0.1:1"
	cfg.R2.AccessKeyID = "k"
	cfg.R2.SecretAccessKey = "s"
	cfg.R2.Bucket = "b"
	cfg.Registry.Valkey.Addr = valkeyAddr
	cfg.GitHub.APIBase = "http://127.0.0.1:1"
	cfg.GitHub.Org = "freeCodeCamp"
	cfg.GitHub.MembershipCacheTTL = time.Minute
	cfg.JWT.SigningKey = "0123456789abcdef0123456789abcdef"
	cfg.JWT.TTL = 15 * time.Minute
	cfg.Aliases.ProductionKeyFormat = "<site>.example.test/production"
	cfg.Aliases.PreviewKeyFormat = "<site>.example.test/preview"
	cfg.Cleanup.TrashPrefix = "_trash/"
	cfg.Cleanup.RecoveryDays = 7
	cfg.Cleanup.Grace = 72 * time.Hour
	cfg.Registry.AuthzTeam = "staff"
	cfg.Repo.Org = "freeCodeCamp-Universe"
	return cfg
}

func startDeps(t *testing.T) (dsn, valkeyAddr string) {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, testPostgresImage,
		postgres.WithDatabase("artemis_boot"),
		postgres.WithUsername("artemis"),
		postgres.WithPassword("artemis"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, err = pgC.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	vkC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = vkC.Terminate(ctx) })

	host, err := vkC.Host(ctx)
	require.NoError(t, err)
	mapped, err := vkC.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	return dsn, fmt.Sprintf("%s:%s", host, mapped.Port())
}

func TestRunWith_BootsAndShutsDownCleanly(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)
	cfg := bootCfg(t, dsn, valkeyAddr, port)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond, "server never started listening")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "a signalled shutdown is a clean exit")
	case <-time.After(30 * time.Second):
		t.Fatal("runWith did not return after context cancellation")
	}
}

func TestRunWith_WarnsWhenPostgresIsWiredWithoutHatchet(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))
	require.Empty(t, cfg.Hatchet.Addr)
	got := trapCaptures(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool { return got.has("outbox.relay.absent") }, 60*time.Second, 200*time.Millisecond,
		"finalize and alias writes enqueue site.changed whenever Postgres is wired, but the relay and "+
			"gc-site are both gated on HATCHET_ADDR, so this is the one shape where the backlog alert "+
			"cannot fire for itself")

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err,
			"a Postgres-backed boot without hatchet is legal today; this is a warning, not a refusal")
	case <-time.After(30 * time.Second):
		t.Fatal("runWith did not return after context cancellation")
	}
}

func TestRunWith_BackfillOnBootExitsWithoutServing(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)
	cfg := bootCfg(t, dsn, valkeyAddr, port)
	cfg.BackfillOnBoot = true

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	err := runWith(ctx, cfg)
	require.Error(t, err, "backfill against an unreachable R2 endpoint must surface, not hang")
}

func TestRunWith_BackfillWithoutDatabaseIsRejected(t *testing.T) {
	_, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, "", valkeyAddr, freePort(t))
	cfg.BackfillOnBoot = true

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "BACKFILL_ON_BOOT",
		"backfill without a database must be refused by name, not fail obscurely")
}

func TestRunWith_ImportsExistingValkeySitesIntoPostgres(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)

	store, err := vkstore.New(context.Background(), vkstore.Config{Addr: valkeyAddr})
	require.NoError(t, err)
	_, err = store.Register(context.Background(), "seeded", []string{"staff"}, "tester")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	cfg := bootCfg(t, dsn, valkeyAddr, port)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("runWith did not return")
	}
}

func TestRunWith_RepoFeatureEnabledWiresAppClient(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)
	cfg := bootCfg(t, dsn, valkeyAddr, port)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	cfg.Repo.App.AppID = "123456"
	cfg.Repo.App.InstallationID = "654321"
	cfg.Repo.App.PrivateKeyPEM = string(pemBytes)
	require.True(t, cfg.Repo.Enabled())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("runWith did not return")
	}
}

func TestRunWith_RejectsMalformedAppKey(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))
	cfg.Repo.App.AppID = "123456"
	cfg.Repo.App.InstallationID = "654321"
	cfg.Repo.App.PrivateKeyPEM = "not-a-pem"

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "github app signer")
}

func TestRunWith_RejectsBadDeployPrefixFormat(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))
	cfg.DeployPrefixFormat = "no-tokens-here/"

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "deploy prefix template")
}

func TestRunWith_RejectsShortJWTKey(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))
	cfg.JWT.SigningKey = "short"

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "jwt signer")
}

func TestRunWith_UnreachableValkeyFails(t *testing.T) {
	cfg := bootCfg(t, "", "127.0.0.1:1", freePort(t))

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "registry")
}

const stubHatchetToken = "eyJhbGciOiAiSFMyNTYiLCAidHlwIjogIkpXVCJ9.eyJzdWIiOiAiNzA3ZDA4NTUtODBhYi00ZTFmLWExNTYtZjFjNDU0NmNiZjUyIiwgImdycGNfYnJvYWRjYXN0X2FkZHJlc3MiOiAiMTI3LjAuMC4xOjEiLCAic2VydmVyX3VybCI6ICJodHRwOi8vMTI3LjAuMC4xOjEiLCAiZXhwIjogNDEwMjQ0NDgwMH0.c2ln"

func TestRunWith_WiresWorkerAndRelayWhenHatchetConfigured(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)
	cfg := bootCfg(t, dsn, valkeyAddr, port)
	cfg.Hatchet.Addr = "127.0.0.1:1"
	cfg.Hatchet.ClientToken = stubHatchetToken

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond,
		"an unreachable hatchet engine must not stop the HTTP server from serving")

	cancel()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("runWith did not return after cancellation")
	}
}

func TestRunWith_SentryEnabledPathBoots(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)
	cfg := bootCfg(t, dsn, valkeyAddr, port)
	cfg.Sentry.DSN = "https://publickey@o0.ingest.sentry.io/0"
	cfg.Sentry.Environment = "test"
	cfg.Sentry.TracesSampleRate = 0

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWith(ctx, cfg) }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 60*time.Second, 200*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("runWith did not return")
	}
}

func TestRun_BootsFromEnvAndExitsOnSigterm(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	port := freePort(t)

	configtest.Hermetic(t, config.EnvKeys(), map[string]string{
		"PORT":                        strconv.Itoa(port),
		"DATABASE_URL":                dsn,
		"VALKEY_ADDR":                 valkeyAddr,
		"R2_ENDPOINT":                 "http://127.0.0.1:1",
		"R2_ACCESS_KEY_ID":            "k",
		"R2_SECRET_ACCESS_KEY":        "s",
		"R2_BUCKET":                   "b",
		"GH_CLIENT_ID":                "cid",
		"JWT_SIGNING_KEY":             "0123456789abcdef0123456789abcdef",
		"DEPLOY_PREFIX_FORMAT":        "<site>.example.test/deploys/<ts>-<sha>/",
		"ALIAS_PRODUCTION_KEY_FORMAT": "<site>.example.test/production",
		"ALIAS_PREVIEW_KEY_FORMAT":    "<site>.example.test/preview",
		"LOG_LEVEL":                   "error",
		"SENTRY_DSN":                  "https://publickey@o0.ingest.sentry.io/0",
		"ENVIRONMENT":                 "test",
		"SENTRY_TRACES_SAMPLE_RATE":   "0",
	})

	done := make(chan error, 1)
	go func() { done <- run() }()

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 90*time.Second, 200*time.Millisecond, "run() never reached a listening server")

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))

	select {
	case err := <-done:
		require.NoError(t, err, "SIGTERM must drain into a clean shutdown")
	case <-time.After(45 * time.Second):
		t.Fatal("run() did not return after SIGTERM")
	}
}

func TestRun_InvalidConfigIsRejected(t *testing.T) {
	t.Setenv("R2_ENDPOINT", "")
	t.Setenv("VALKEY_ADDR", "")

	require.Error(t, run(), "a config without required vars must fail fast")
}

func TestRunWith_ListenFailureSurfaces(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)

	occupied, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer func() { _ = occupied.Close() }()
	port := occupied.Addr().(*net.TCPAddr).Port

	cfg := bootCfg(t, dsn, valkeyAddr, port)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = runWith(ctx, cfg)
	require.ErrorContains(t, err, "listen", "a bound port must surface through errCh")
}

func TestRunWith_UnreachablePostgresSurfaces(t *testing.T) {
	cfg := bootCfg(t, "postgres://nobody@127.0.0.1:1/none?sslmode=disable", "127.0.0.1:1", freePort(t))
	cfg.PGConnectRetryWindow = 0

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "open postgres")
}

func TestRunWith_RepoFeatureWithoutDatabaseFails(t *testing.T) {
	_, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, "", valkeyAddr, freePort(t))

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cfg.Repo.App.AppID = "1"
	cfg.Repo.App.InstallationID = "2"
	cfg.Repo.App.PrivateKeyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))

	err = runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "repo-request store")
}

func TestRunWith_BadAliasFormatFailsGCWiring(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))
	cfg.Aliases.ProductionKeyFormat = "no-token/production"

	err := runWith(context.Background(), cfg)
	require.ErrorContains(t, err, "wire gc")
}

func TestRun_SentryInitFailureIsFatal(t *testing.T) {
	t.Setenv("VALKEY_ADDR", "127.0.0.1:1")
	t.Setenv("R2_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("R2_ACCESS_KEY_ID", "k")
	t.Setenv("R2_SECRET_ACCESS_KEY", "s")
	t.Setenv("GH_CLIENT_ID", "cid")
	t.Setenv("JWT_SIGNING_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("SENTRY_DSN", "://not-a-dsn")

	require.Error(t, run())
}

func TestRunWith_MigrationFailureAborts(t *testing.T) {
	dsn, valkeyAddr := startDeps(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TABLE schema_migrations (wrong_column INT NOT NULL)`)
	require.NoError(t, err, "seed a ledger table with an incompatible shape")
	pool.Close()

	cfg := bootCfg(t, dsn, valkeyAddr, freePort(t))

	err = runWith(ctx, cfg)
	require.ErrorContains(t, err, "postgres",
		"a failed migration must abort boot, never serve on an unmigrated schema")
}

func TestNewGCWiring_SharesOneSiteLockerWithSiteGC(t *testing.T) {
	dsn, _ := startDeps(t)
	ctx := context.Background()

	db, cleanup, err := openPostgres(ctx, &config.Config{DatabaseURL: dsn})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	cfg := &config.Config{DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/"}
	cfg.Cleanup.TrashPrefix = "_trash/"
	cfg.Aliases.ProductionKeyFormat = "<site>/production"
	cfg.Aliases.PreviewKeyFormat = "<site>/preview"

	gcw, err := newGCWiring(cfg, pg.NewRepo(db), nil, nil)
	require.NoError(t, err)

	require.NotNil(t, gcw.Reconciler.Locker,
		"a reconciler with no Locker refuses every live run")
	require.Equal(t, gcw.SiteGC.Locker, gcw.Reconciler.Locker,
		"reconcile and gc-site must serialize on the same per-site lock or they can race")
}
