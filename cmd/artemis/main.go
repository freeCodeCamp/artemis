// Command artemis is the Universe static-apps deploy proxy.
//
// It loads its configuration from environment variables, mounts a
// sites→teams authorization map, and serves the API surface defined in
// ADR-016 §API surface. R2 admin S3 credentials live exclusively in this
// process; staff and CI never see them.
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
	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/freeCodeCamp/artemis/internal/server"
	"github.com/freeCodeCamp/artemis/internal/sites"
)

func main() {
	if err := run(); err != nil {
		slog.Error("artemis: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	configureLogger(cfg.LogLevel)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Sites loader + hot reload.
	siteLoader, err := sites.New(cfg.SitesYAMLPath)
	if err != nil {
		return fmt.Errorf("load sites.yaml: %w", err)
	}
	defer siteLoader.Close()
	if err := siteLoader.Watch(rootCtx); err != nil {
		return fmt.Errorf("watch sites.yaml: %w", err)
	}

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

	h := &handler.Handlers{
		GH:                 ghClient,
		JWT:                signer,
		Sites:              siteLoader,
		R2:                 r2Client,
		AliasProductionFmt: cfg.Aliases.ProductionKeyFormat,
		AliasPreviewFmt:    cfg.Aliases.PreviewKeyFormat,
		DeployPrefixFmt:    cfg.DeployPrefixFormat,
		NewDeployID:        r2.NewDeployID,
		Now:                time.Now,
	}

	addr := ":" + strconv.Itoa(cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(h),
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
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	slog.Info("artemis: shutdown complete")
	return nil
}

func configureLogger(level string) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
}
