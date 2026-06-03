// Command artemis is the Universe static-apps deploy proxy.
//
// It loads its configuration from environment variables, mounts a
// sites→teams authorization map, and serves the deploy/promote/rollback
// API. R2 admin S3 credentials live exclusively in this process; staff
// and CI never see them.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
	repovalkey "github.com/freeCodeCamp/artemis/internal/reporequest/valkey"
	"github.com/freeCodeCamp/artemis/internal/server"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/prometheus/client_golang/prometheus"
)

// Build-time identity, injected via -ldflags "-X main.version=... -X main.commit=...".
// Defaults match the Dockerfile ARG defaults so a bare `go build` is still useful.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(); err != nil {
		observability.CaptureFatal(err) // no-op unless Sentry was initialised
		slog.Error("artemis: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// Log version BEFORE config.Load() so a misconfigured deploy still leaves
	// a version breadcrumb in container logs (default slog handler is fine
	// for this single line; configureLogger swaps it in below).
	slog.Info("artemis: starting", "version", version, "commit", commit)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Sentry must come up before the logger so the slog bridge can tee
	// into it. Release ties every event to the build identity already
	// injected via -ldflags. Empty DSN => disabled, flush is a no-op.
	release := fmt.Sprintf("artemis@%s+%s", version, commit)
	flushSentry, sentryEnabled, err := observability.Init(observability.Config{
		DSN:              cfg.Sentry.DSN,
		Environment:      cfg.Sentry.Environment,
		Release:          release,
		TracesSampleRate: cfg.Sentry.TracesSampleRate,
		Debug:            cfg.Sentry.Debug,
	})
	if err != nil {
		return fmt.Errorf("init sentry: %w", err)
	}
	defer flushSentry()

	logLevel := parseLogLevel(cfg.LogLevel)
	var sentryLog slog.Handler
	if sentryEnabled {
		sentryLog = observability.NewSlogHandler(logLevel)
	}
	configureLogger(logLevel, sentryLog)
	if sentryEnabled {
		slog.Info("sentry enabled",
			"environment", cfg.Sentry.Environment,
			"release", release,
			"tracesSampleRate", cfg.Sentry.TracesSampleRate,
		)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pgDB, pgCleanup, err := openPostgres(rootCtx, cfg)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer pgCleanup()
	if pgDB != nil {
		slog.Info("postgres: connected, migrations applied")
	} else {
		slog.Info("postgres disabled (DATABASE_URL unset); deploy-only mode, GC off")
	}

	registryStore, registryReader, registryCleanup, err := openRegistry(rootCtx, cfg)
	if err != nil {
		return fmt.Errorf("open registry: %w", err)
	}
	defer registryCleanup()

	// R2 client.
	r2Client, err := r2.New(rootCtx, r2.Config{
		Endpoint:        cfg.R2.Endpoint,
		AccessKeyID:     cfg.R2.AccessKeyID,
		SecretAccessKey: cfg.R2.SecretAccessKey,
		Bucket:          cfg.R2.Bucket,
		Region:          "auto",
	})
	if err != nil {
		return fmt.Errorf("init r2: %w", err)
	}

	// GitHub identity client.
	ghClient := auth.NewGitHubClient(auth.GitHubClientConfig{
		APIBase:  cfg.GitHub.APIBase,
		Org:      cfg.GitHub.Org,
		CacheTTL: cfg.GitHub.MembershipCacheTTL,
	})

	// JWT signer.
	signer, err := auth.NewDeploySessionSigner(cfg.JWT.SigningKey, cfg.JWT.TTL)
	if err != nil {
		return fmt.Errorf("init jwt signer: %w", err)
	}

	// Repo-creation feature (optional). Wired only when the Apollo-11 App
	// credentials are configured; absent → feature off, /api/repo*
	// routes left unmounted. repoGH probes membership in the Universe org
	// (cfg.Repo.Org), distinct from ghClient's site-registry org.
	var (
		repoStore *repovalkey.Store
		repoGH    *auth.GitHubClient
		appClient *githubapp.Client
	)
	if cfg.Repo.Enabled() {
		appSigner, err := githubapp.NewAppJWTSigner(cfg.Repo.App.AppID, cfg.Repo.App.PrivateKeyPEM)
		if err != nil {
			return fmt.Errorf("init github app signer: %w", err)
		}
		appClient, err = githubapp.NewClient(githubapp.ClientConfig{
			APIBase:        cfg.GitHub.APIBase,
			Org:            cfg.Repo.Org,
			InstallationID: cfg.Repo.App.InstallationID,
			Signer:         appSigner,
		})
		if err != nil {
			return fmt.Errorf("init github app client: %w", err)
		}
		repoStore, err = repovalkey.New(rootCtx, repovalkey.Config{
			Addr:     cfg.Registry.Valkey.Addr,
			Password: cfg.Registry.Valkey.Password,
		})
		if err != nil {
			return fmt.Errorf("open repo-request store: %w", err)
		}
		defer func() { _ = repoStore.Close() }()
		repoGH = auth.NewGitHubClient(auth.GitHubClientConfig{
			APIBase:  cfg.GitHub.APIBase,
			Org:      cfg.Repo.Org,
			CacheTTL: cfg.GitHub.MembershipCacheTTL,
		})
		slog.Info("repo-creation feature enabled",
			"org", cfg.Repo.Org,
			"createTeam", cfg.Repo.CreateAuthzTeam,
			"approveTeam", cfg.Repo.ApproveAuthzTeam,
		)
	}

	deployPrefix, err := handler.NewDeployPrefixTemplate(cfg.DeployPrefixFormat)
	if err != nil {
		return fmt.Errorf("parse deploy prefix template: %w", err)
	}

	metricsReg := prometheus.NewRegistry()
	metrics := handler.NewMetrics(metricsReg)
	handler.SetMetrics(metrics)
	registryReader.SetOnRefreshError(func(err error) {
		metrics.RegistryRefreshFailures.Inc()
		observability.CaptureBackground("registry.refresh", err)
	})

	var gcw *gcWiring
	if pgDB != nil {
		gcw, err = newGCWiring(cfg, pg.NewRepo(pgDB), r2Client, gc.NewMetrics(metricsReg))
		if err != nil {
			return fmt.Errorf("wire gc: %w", err)
		}
		slog.Info("gc: wired",
			"siteGCReady", gcw.SiteGC != nil,
			"blastCap", cfg.Cleanup.BlastCap,
			"retentionDays", cfg.Cleanup.RetentionDays,
			"dryRun", cfg.Cleanup.DryRun,
		)
	}

	var pgRepo *pg.Repo
	if gcw != nil {
		pgRepo = gcw.Repo
	}

	var hatchetAdapter *hatchet.Adapter
	workerErrCh := make(chan error, 1)
	if gcw != nil && cfg.Hatchet.Addr != "" {
		hatchetAdapter = hatchet.New(hatchet.Config{
			Token:      cfg.Hatchet.ClientToken,
			Addr:       cfg.Hatchet.Addr,
			WorkerName: "artemis",
		})
		workerRuntime := worker.NewRuntime(hatchetAdapter)
		if err := registerGCWorkflows(workerRuntime, gcw, cfg.Cleanup.DryRun); err != nil {
			return fmt.Errorf("register gc workflows: %w", err)
		}
		go func() {
			slog.Info("worker: starting", "addr", cfg.Hatchet.Addr)
			workerErrCh <- workerRuntime.Start(rootCtx)
		}()

		relay := &worker.Relay{Source: pgRepo, Publisher: hatchetAdapter, Batch: 100, Now: time.Now}
		go runRelayLoop(rootCtx, relay, relayInterval)
		slog.Info("outbox relay: started", "interval", relayInterval)
	}

	h := &handler.Handlers{
		GH:                   ghClient,
		JWT:                  signer,
		Sites:                registryReader,
		Registry:             registryStore,
		Health:               registryStore,
		R2:                   r2Client,
		AliasProductionFmt:   cfg.Aliases.ProductionKeyFormat,
		AliasPreviewFmt:      cfg.Aliases.PreviewKeyFormat,
		DeployPrefix:         deployPrefix,
		TrashPrefixBase:      cfg.Cleanup.TrashPrefix,
		UploadMaxBytes:       cfg.UploadMaxBytes,
		RegistryAuthzTeam:    cfg.Registry.AuthzTeam,
		RepoOrg:              cfg.Repo.Org,
		RepoCreateAuthzTeam:  cfg.Repo.CreateAuthzTeam,
		RepoApproveAuthzTeam: cfg.Repo.ApproveAuthzTeam,
		NewDeployID:          r2.NewDeployID,
		Now:                  time.Now,
		Metrics:              metrics,
	}

	// Assign the repo interface deps only when enabled — assigning a
	// typed-nil pointer to an interface field would make RepoEnabled()
	// (which compares != nil) true and mount routes onto nil deps.
	if cfg.Repo.Enabled() {
		h.RepoGH = repoGH
		h.Repos = repoStore
		h.GitHubApp = appClient
	}

	if pgRepo != nil {
		h.Outbox = pgRepo
		h.Tombstones = pgRepo
	}

	addr := ":" + strconv.Itoa(cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(h, metricsReg),
		ReadHeaderTimeout: 10 * time.Second,
		// No global ReadTimeout — uploads are streamed and may run long.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("artemis: listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		slog.Info("artemis: shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case err := <-workerErrCh:
		if err != nil {
			return fmt.Errorf("worker: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	slog.Info("artemis: shutdown complete")
	return nil
}

func openPostgres(ctx context.Context, cfg *config.Config) (*pg.DB, func(), error) {
	if !cfg.GCEnabled() {
		return nil, func() {}, nil
	}
	db, err := pg.New(ctx, pg.Config{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	if err := pg.Migrate(ctx, db.Pool); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	return db, db.Close, nil
}

// openRegistry constructs the Valkey-backed registry store + reader.
// The store is the Writer surface used by /api/site/{register,update,
// delete}; the reader is the Reader surface used by every read-side
// handler. Cleanup MUST be called on shutdown to close the connection.
func openRegistry(ctx context.Context, cfg *config.Config) (*valkey.Store, *valkey.Reader, func(), error) {
	store, err := valkey.New(ctx, valkey.Config{
		Addr:     cfg.Registry.Valkey.Addr,
		Password: cfg.Registry.Valkey.Password,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("valkey: %w", err)
	}
	reader, err := valkey.NewReader(ctx, store, valkey.DefaultRefreshFallback)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, fmt.Errorf("valkey reader: %w", err)
	}
	return store, reader, func() { _ = store.Close() }, nil
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// configureLogger installs the JSON stdout handler. When extra is
// non-nil (the Sentry Logs bridge) records are teed to both — stdout
// stays the source of truth for Loki while Sentry mirrors them.
func configureLogger(lvl slog.Level, extra slog.Handler) {
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	if extra != nil {
		h = observability.NewMultiHandler(h, extra)
	}
	slog.SetDefault(slog.New(h))
}
