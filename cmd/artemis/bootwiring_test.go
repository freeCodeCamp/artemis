package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
)

func baseCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Aliases.ProductionKeyFormat = "<site>/production"
	cfg.Aliases.PreviewKeyFormat = "<site>/preview"
	cfg.Cleanup.TrashPrefix = "_trash/"
	cfg.Cleanup.RecoveryDays = 7
	cfg.UploadMaxBytes = 1024
	cfg.Registry.AuthzTeam = "staff"
	cfg.Repo.Org = "freeCodeCamp-Universe"
	cfg.Repo.CreateAuthzTeam = "staff"
	cfg.Repo.ApproveAuthzTeam = "approvers"
	cfg.Repo.AuditReadAuthzTeam = "staff"
	return cfg
}

func TestBuildHandlers_CopiesConfigOntoHandlers(t *testing.T) {
	t.Parallel()

	cfg := baseCfg()
	h := buildHandlers(cfg, handlerDeps{})

	require.Equal(t, "<site>/production", h.AliasProductionFmt)
	require.Equal(t, "<site>/preview", h.AliasPreviewFmt)
	require.Equal(t, "_trash/", h.TrashPrefixBase)
	require.Equal(t, 7*24*time.Hour, h.TrashRecovery)
	require.EqualValues(t, 1024, h.UploadMaxBytes)
	require.Equal(t, "staff", h.RegistryAuthzTeam)
	require.Equal(t, "freeCodeCamp-Universe", h.RepoOrg)
	require.Equal(t, "staff", h.RepoCreateAuthzTeam)
	require.Equal(t, "approvers", h.RepoApproveAuthzTeam)
	require.Equal(t, "staff", h.AuditReadAuthzTeam)
	require.NotNil(t, h.NewDeployID)
	require.NotNil(t, h.Now)
}

func TestBuildHandlers_LeavesRepoDepsNilWhenFeatureDisabled(t *testing.T) {
	t.Parallel()

	cfg := baseCfg()
	require.False(t, cfg.Repo.Enabled())

	h := buildHandlers(cfg, handlerDeps{repoStore: stubRepoStore{}})

	require.Nil(t, h.Repos,
		"assigning a repo store while the feature is off would mount /api/repo* onto unconfigured deps")
	require.Nil(t, h.GitHubApp)
}

func TestBuildHandlers_AssignsRepoDepsWhenFeatureEnabled(t *testing.T) {
	t.Parallel()

	cfg := baseCfg()
	cfg.Repo.App.AppID = "123"
	cfg.Repo.App.InstallationID = "456"
	cfg.Repo.App.PrivateKeyPEM = "pem"
	require.True(t, cfg.Repo.Enabled())

	h := buildHandlers(cfg, handlerDeps{repoStore: stubRepoStore{}})

	require.NotNil(t, h.Repos)
}

func TestNewHTTPServer_SetsUploadFriendlyTimeouts(t *testing.T) {
	t.Parallel()

	srv := newHTTPServer(":9999", http.NotFoundHandler())

	require.Equal(t, ":9999", srv.Addr)
	require.Equal(t, 10*time.Second, srv.ReadHeaderTimeout)
	require.Zero(t, srv.WriteTimeout,
		"a non-zero WriteTimeout would cut long streaming uploads")
	require.Equal(t, 120*time.Second, srv.IdleTimeout)
	require.NotNil(t, srv.Handler)
}

func TestAwaitShutdown_ReturnsNilOnSignal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, awaitShutdown(ctx, make(chan error), make(chan error)))
}

func TestAwaitShutdown_WrapsListenError(t *testing.T) {
	t.Parallel()

	errCh := make(chan error, 1)
	errCh <- errors.New("port busy")

	err := awaitShutdown(context.Background(), errCh, make(chan error))

	require.ErrorContains(t, err, "listen")
	require.ErrorContains(t, err, "port busy")
}

func TestAwaitShutdown_WrapsWorkerError(t *testing.T) {
	t.Parallel()

	workerCh := make(chan error, 1)
	workerCh <- errors.New("engine gone")

	err := awaitShutdown(context.Background(), make(chan error), workerCh)

	require.ErrorContains(t, err, "worker")
	require.ErrorContains(t, err, "engine gone")
}

func TestAwaitShutdown_CleanWorkerExitIsNotAnError(t *testing.T) {
	t.Parallel()

	workerCh := make(chan error, 1)
	workerCh <- nil

	require.NoError(t, awaitShutdown(context.Background(), make(chan error), workerCh))
}

type stubRepoStore struct{ handler.RepoStore }

func TestExitCodeFor(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0, exitCodeFor(nil))
	require.Equal(t, 0, exitCodeFor(context.Canceled),
		"a signalled shutdown is not a crash")
	require.Equal(t, 0, exitCodeFor(fmt.Errorf("shutting down: %w", context.Canceled)))
	require.Equal(t, 1, exitCodeFor(errors.New("boom")))
}

func TestMain_ExitsZeroOnCleanShutdown(t *testing.T) {
	if os.Getenv("ARTEMIS_MAIN_SUBPROCESS") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMain_ExitsZeroOnCleanShutdown")
	cmd.Env = append(os.Environ(),
		"ARTEMIS_MAIN_SUBPROCESS=1",
		"DATABASE_URL=",
		"SENTRY_DSN=",
		"VALKEY_ADDR=127.0.0.1:1",
		"R2_ENDPOINT=http://127.0.0.1:1",
		"R2_ACCESS_KEY_ID=k",
		"R2_SECRET_ACCESS_KEY=s",
		"GH_CLIENT_ID=cid",
		"JWT_SIGNING_KEY=0123456789abcdef0123456789abcdef",
	)
	err := cmd.Run()

	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee, "an unreachable registry must exit non-zero")
	require.Equal(t, 1, ee.ExitCode(), "boot.fatal must map to exit 1")
}
