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
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/freeCodeCamp/artemis/internal/auth"
	"github.com/freeCodeCamp/artemis/internal/backfill"
	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/githubapp"
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/observability"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/registry/valkey"
	"github.com/freeCodeCamp/artemis/internal/server"
	"github.com/freeCodeCamp/artemis/internal/teamcache"
	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

// Build-time identity, injected via -ldflags "-X main.version=... -X main.commit=...".
// Defaults match the Dockerfile ARG defaults so a bare `go build` is still useful.
var (
	version = "dev"
	commit  = "unknown"
)

const bootPhaseTimeout = 20 * time.Second

func dispatchSubcommand(ctx context.Context, out io.Writer, args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case driftReportCommand:
		if len(args) > 1 {
			return true, fmt.Errorf("%s takes no arguments, got %q: it always sweeps every registered site",
				driftReportCommand, strings.Join(args[1:], " "))
		}
		if err := runDriftReport(ctx, out); err != nil {
			return true, fmt.Errorf("drift report failed: %w", err)
		}
		return true, nil
	case reconcileCommand:
		if err := runReconcileCLI(ctx, out, args[1:]); err != nil {
			return true, fmt.Errorf("reconcile failed: %w", err)
		}
		return true, nil
	default:
		return true, fmt.Errorf("unknown subcommand %q: expected %s or %s",
			args[0], driftReportCommand, reconcileCommand)
	}
}

func main() {
	if handled, err := dispatchSubcommand(context.Background(), os.Stdout, os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "artemis:", err)
			os.Exit(1)
		}
		return
	}
	if code := exitCodeFor(run()); code != 0 {
		os.Exit(code)
	}
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		slog.Info("boot.aborted", "err", err)
		return 0
	}
	observability.CaptureFatal(err)
	slog.Error("boot.fatal", "err", err)
	return 1
}

func run() error {
	// Log version BEFORE config.Load() so a misconfigured deploy still leaves
	// a version breadcrumb in container logs (default slog handler is fine
	// for this single line; configureLogger swaps it in below).
	slog.Info("boot.starting", "version", version, "commit", commit)

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
		slog.Info("sentry.enabled",
			"environment", cfg.Sentry.Environment,
			"release", release,
			"tracesSampleRate", cfg.Sentry.TracesSampleRate,
		)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return runWith(rootCtx, cfg)
}

func runWith(rootCtx context.Context, cfg *config.Config) error {
	pgDB, pgCleanup, err := openPostgres(rootCtx, cfg)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer pgCleanup()
	if pgDB != nil {
		slog.Info("postgres.connected")
	} else {
		slog.Info("postgres.disabled")
	}

	registryWriter, registryReader, registryHealth, registryCleanup, err := openRegistry(rootCtx, cfg, pgDB)
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

	githubTeamCache, teamCacheCleanup, err := openTeamCache(rootCtx, cfg)
	if err != nil {
		return fmt.Errorf("open team cache: %w", err)
	}
	defer teamCacheCleanup()

	// GitHub identity client.
	ghClient := auth.NewGitHubClient(auth.GitHubClientConfig{
		APIBase:   cfg.GitHub.APIBase,
		Org:       cfg.GitHub.Org,
		CacheTTL:  cfg.GitHub.MembershipCacheTTL,
		TeamCache: githubTeamCache,
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
		repoStore handler.RepoStore
		appClient *githubapp.Client
	)
	repoGH := auth.NewGitHubClient(auth.GitHubClientConfig{
		APIBase:  cfg.GitHub.APIBase,
		Org:      cfg.Repo.Org,
		CacheTTL: cfg.GitHub.MembershipCacheTTL,
	})
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
		repoStore, err = openRepoQueue(pgDB)
		if err != nil {
			return fmt.Errorf("open repo-request store: %w", err)
		}
		slog.Info("repo.feature.enabled",
			"org", cfg.Repo.Org,
			"createTeam", cfg.Repo.CreateAuthzTeam,
			"approveTeam", cfg.Repo.ApproveAuthzTeam,
		)
	}

	deployPrefix, err := handler.NewDeployPrefixTemplate(cfg.DeployPrefixFormat)
	if err != nil {
		return fmt.Errorf("parse deploy prefix template: %w", err)
	}

	registryReader.SetOnRefreshError(func(err error) {
		observability.CaptureBackground("registry.refresh", err)
	})

	var gcw *gcWiring
	if pgDB != nil {
		lockRepo := pg.NewRepo(pgDB)
		lockRepo.OnLockSessionLost(func() {
			observability.CaptureBackground("lock.session_lost",
				errors.New("the connection holding a per-site advisory lock stopped answering while its "+
					"closure was still running; postgres released the lock and the work in flight had no "+
					"mutual exclusion"))
		})
		gcw, err = newGCWiring(cfg, lockRepo, r2Client, registryWriter)
		if err != nil {
			return fmt.Errorf("wire gc: %w", err)
		}
		slog.Info("gc.wired",
			"siteGCReady", gcw.SiteGC != nil,
			"reservationSweepReady", gcw.Reservations != nil && gcw.NameReleaser != nil,
			"blastCap", cfg.Cleanup.BlastCap,
			"retentionDays", cfg.Cleanup.RetentionDays,
			"dryRun", cfg.Cleanup.DryRun,
		)
	}

	var pgRepo *pg.Repo
	if gcw != nil {
		pgRepo = gcw.Repo
	}

	if cfg.BackfillOnBoot {
		if pgRepo == nil {
			return fmt.Errorf("BACKFILL_ON_BOOT set but DATABASE_URL is unset")
		}
		layout, layoutErr := newGCLayout(cfg.DeployPrefixFormat, cfg.Cleanup.TrashPrefix)
		if layoutErr != nil {
			return fmt.Errorf("backfill layout: %w", layoutErr)
		}
		tails, tailErr := cfg.AliasKeyTails()
		if tailErr != nil {
			return fmt.Errorf("backfill alias formats: %w", tailErr)
		}
		res, err := (&backfill.Backfill{
			Lister: r2Client, Indexer: pgRepo, Now: time.Now,
			SitePrefix: layout.sitePrefix,
			AliasKey: func(dirname, mode string) string {
				if mode == "production" {
					return dirname + "/" + tails[0]
				}
				return dirname + "/" + tails[1]
			},
		}).Run(rootCtx)
		if err != nil {
			return fmt.Errorf("backfill: %w", err)
		}
		slog.Info("backfill.complete",
			"sites", res.Sites, "deploys", res.Deploys, "aliases", res.Aliases)
		return nil
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
		sweepTails, tailErr := cfg.AliasKeyTails()
		if tailErr != nil {
			return fmt.Errorf("drift alias formats: %w", tailErr)
		}
		sweepDrift := func(ctx context.Context) (sweepResult, error) {
			return newReadOnlySweeper(gcw.Reconciler, r2Client, pgRepo,
				pg.NewRegistryStore(pgDB), deployPrefix, r2Client, sweepTails).Run(ctx)
		}
		if err := registerGCWorkflows(workerRuntime, gcw, cfg.Cleanup.DryRun, sweepDrift); err != nil {
			return fmt.Errorf("register gc workflows: %w", err)
		}
		go func() {
			slog.Info("worker.starting", "addr", cfg.Hatchet.Addr)
			workerErrCh <- workerRuntime.Start(rootCtx)
		}()

		relay := &worker.Relay{Source: pgRepo, Publisher: hatchetAdapter, Batch: 100, Now: time.Now}
		go runRelayLoop(rootCtx, relay, pgRepo, relayInterval)
		slog.Info("outbox.relay.started", "interval", relayInterval)
	} else if gcw != nil {
		absent := errors.New("DATABASE_URL is set but HATCHET_ADDR is not: every site.changed row " +
			"enqueued by finalize and alias writes stays unpublished and gc-site never runs")
		slog.Warn("outbox.relay.absent", "err", absent)
		captureBackground("outbox.relay.absent", absent)
	}

	h := buildHandlers(cfg, handlerDeps{
		gh:           ghClient,
		repoGH:       repoGH,
		jwt:          signer,
		sites:        registryReader,
		registry:     registryWriter,
		health:       registryHealth,
		r2:           r2Client,
		deployPrefix: deployPrefix,
		repoStore:    repoStore,
		appClient:    appClient,
	})

	wirePGRepo(h, pgRepo)
	if pgDB != nil {
		h.PGHealth = pgDB
	}

	addr := ":" + strconv.Itoa(cfg.Port)
	srv := newHTTPServer(addr, server.New(h))

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server.listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if pgDB != nil {
		go func() {
			onConcurrentMigrateErr(rootCtx, pg.MigrateConcurrent(rootCtx, pgDB.Pool))
		}()
	}

	if err := awaitShutdown(rootCtx, errCh, workerErrCh); err != nil {
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	slog.Info("server.shutdown.complete")
	return nil
}

func openPostgres(ctx context.Context, cfg *config.Config) (*pg.DB, func(), error) {
	if !cfg.GCEnabled() {
		return nil, func() {}, nil
	}
	db, err := pg.NewWithRetry(ctx, pg.Config{DatabaseURL: cfg.DatabaseURL}, cfg.PGConnectRetryWindow)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	migrateCtx, cancel := context.WithTimeout(ctx, bootPhaseTimeout)
	defer cancel()
	if err := pg.Migrate(migrateCtx, db.Pool); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	return db, db.Close, nil
}

// openRegistry constructs the registry Writer, read-side Reader, and
// health probe. When pgDB is non-nil, pg.RegistryStore is the
// source-of-truth (Writer + Reader source) and Valkey is the
// OnChange-published cache-front transport; otherwise Valkey is the
// source-of-truth. Cleanup MUST be called on shutdown.
func openRegistry(ctx context.Context, cfg *config.Config, pgDB *pg.DB) (registry.Writer, *valkey.Reader, *valkey.Store, func(), error) {
	store, err := valkey.NewWithRetry(ctx, valkey.Config{
		Addr:     cfg.Registry.Valkey.Addr,
		Password: cfg.Registry.Valkey.Password,
	}, cfg.Registry.Valkey.RetryWindow)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("valkey: %w", err)
	}

	var (
		writer registry.Writer    = store
		source valkey.SitesSource = store
	)
	if pgDB != nil {
		pgReg := pg.NewRegistryStore(pgDB).WithOnChange(valkey.PublishOnChange(ctx, store))
		importCtx, cancel := context.WithTimeout(ctx, bootPhaseTimeout)
		imported, err := pgReg.Import(importCtx, store)
		cancel()
		if err != nil {
			_ = store.Close()
			return nil, nil, nil, nil, fmt.Errorf("registry import: %w", err)
		}
		if imported > 0 {
			slog.Info("registry.import.complete", "sites", imported)
		}
		writer = pgReg
		source = pgReg
	}

	reader, err := valkey.NewReaderFromSource(ctx, source, store, valkey.DefaultRefreshFallback)
	if err != nil {
		_ = store.Close()
		return nil, nil, nil, nil, fmt.Errorf("valkey reader: %w", err)
	}
	return writer, reader, store, func() { _ = store.Close() }, nil
}

func openTeamCache(ctx context.Context, cfg *config.Config) (auth.TeamCache, func(), error) {
	if cfg.Registry.Valkey.Addr == "" {
		return nil, func() {}, nil
	}
	client, err := valkey.NewClientWithRetry(ctx, valkey.Config{
		Addr:     cfg.Registry.Valkey.Addr,
		Password: cfg.Registry.Valkey.Password,
	}, cfg.Registry.Valkey.RetryWindow)
	if err != nil {
		return nil, func() {}, err
	}
	return teamcache.New(client, cfg.GitHub.MembershipCacheTTL), func() { _ = client.Close() }, nil
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
	var h slog.Handler = observability.NewScrubbingHandler(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}),
	)
	if extra != nil {
		h = observability.NewMultiHandler(h, extra)
	}
	slog.SetDefault(slog.New(telemetry.NewLogHandler(h)))
}

type handlerDeps struct {
	gh           handler.GitHubAuthenticator
	repoGH       handler.GitHubAuthenticator
	jwt          handler.DeployJWTSigner
	sites        handler.SitesProvider
	registry     handler.RegistryWriter
	health       handler.RegistryHealth
	r2           handler.R2Store
	deployPrefix handler.DeployPrefixTemplate
	repoStore    handler.RepoStore
	appClient    *githubapp.Client
}

func buildHandlers(cfg *config.Config, d handlerDeps) *handler.Handlers {
	h := &handler.Handlers{
		GH:                 d.gh,
		JWT:                d.jwt,
		Sites:              d.sites,
		Registry:           d.registry,
		Health:             d.health,
		R2:                 d.r2,
		AliasProductionFmt: cfg.Aliases.ProductionKeyFormat,
		AliasPreviewFmt:    cfg.Aliases.PreviewKeyFormat,

		PublicProductionURLFmt: cfg.Aliases.ProductionURLFormat,
		PublicPreviewURLFmt:    cfg.Aliases.PreviewURLFormat,
		DeployPrefix:           d.deployPrefix,
		TrashPrefixBase:        cfg.Cleanup.TrashPrefix,
		TrashRecovery:          time.Duration(cfg.Cleanup.RecoveryDays) * 24 * time.Hour,
		UploadMaxBytes:         cfg.UploadMaxBytes,
		RegistryAuthzTeam:      cfg.Registry.AuthzTeam,
		RepoOrg:                cfg.Repo.Org,
		RepoCreateAuthzTeam:    cfg.Repo.CreateAuthzTeam,
		RepoApproveAuthzTeam:   cfg.Repo.ApproveAuthzTeam,
		AuditReadAuthzTeam:     cfg.Repo.AuditReadAuthzTeam,
		NewDeployID:            r2.NewDeployID,
		Now:                    time.Now,
	}
	h.RepoGH = d.repoGH
	if cfg.Repo.Enabled() {
		h.Repos = d.repoStore
		h.GitHubApp = d.appClient
	}
	if rs, ok := d.registry.(handler.ReservationStore); ok {
		h.Reservations = rs
		h.ReservationGrace = cfg.Registry.ReservationGrace
	}
	if nr, ok := d.registry.(handler.NameReleaser); ok {
		h.NameReleaser = nr
	}
	return h
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
}

func awaitShutdown(ctx context.Context, errCh, workerErrCh <-chan error) error {
	select {
	case <-ctx.Done():
		slog.Info("server.shutdown.signal")
		return nil
	case err := <-errCh:
		return fmt.Errorf("listen: %w", err)
	case err := <-workerErrCh:
		if err != nil {
			return fmt.Errorf("worker: %w", err)
		}
		return nil
	}
}
