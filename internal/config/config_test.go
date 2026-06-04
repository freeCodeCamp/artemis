package config

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requiredEnv lists the env vars that have no default and must be set.
func requiredEnv() map[string]string {
	return map[string]string{
		"R2_ENDPOINT":          "https://acct.r2.cloudflarestorage.com",
		"R2_ACCESS_KEY_ID":     "ak",
		"R2_SECRET_ACCESS_KEY": "sk",
		"GH_CLIENT_ID":         "Iv1.deadbeef",
		"JWT_SIGNING_KEY":      "0123456789abcdef0123456789abcdef",
		"VALKEY_ADDR":          "valkey.artemis.svc:6379",
	}
}

func TestLoad_AllDefaults(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, "https://acct.r2.cloudflarestorage.com", cfg.R2.Endpoint)
	assert.Equal(t, "ak", cfg.R2.AccessKeyID)
	assert.Equal(t, "sk", cfg.R2.SecretAccessKey)
	assert.Equal(t, "universe-static-apps-01", cfg.R2.Bucket)

	assert.Equal(t, "Iv1.deadbeef", cfg.GitHub.ClientID)
	assert.Equal(t, "freeCodeCamp", cfg.GitHub.Org)
	assert.Equal(t, "https://api.github.com", cfg.GitHub.APIBase)
	assert.Equal(t, 5*time.Minute, cfg.GitHub.MembershipCacheTTL)

	assert.Equal(t, "0123456789abcdef0123456789abcdef", cfg.JWT.SigningKey)
	assert.Equal(t, 15*time.Minute, cfg.JWT.TTL)

	assert.Equal(t, "<site>/production", cfg.Aliases.ProductionKeyFormat)
	assert.Equal(t, "<site>/preview", cfg.Aliases.PreviewKeyFormat)
	assert.Equal(t, "<site>/deploys/<ts>-<sha>/", cfg.DeployPrefixFormat)
	assert.EqualValues(t, 100*1024*1024, cfg.UploadMaxBytes)

	assert.Equal(t, "info", cfg.LogLevel)

	assert.Equal(t, "staff", cfg.Registry.AuthzTeam)
	assert.Equal(t, "valkey.artemis.svc:6379", cfg.Registry.Valkey.Addr)
	assert.Empty(t, cfg.Registry.Valkey.Password)
}

func TestLoad_GitHubAPIBaseValidation(t *testing.T) {
	valid := []string{
		"",                         // unset -> default https://api.github.com
		"https://api.github.com",   // canonical
		"https://ghe.corp.example", // GitHub Enterprise
		"http://127.0.0.1:8080",    // loopback recording proxy
		"http://localhost:3000",    // loopback by name
		"http://[::1]:9090",        // loopback v6
	}
	for _, base := range valid {
		t.Run("valid/"+base, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			t.Setenv("GH_API_BASE", base)
			_, err := Load()
			require.NoError(t, err)
		})
	}

	invalid := []string{
		"http://evil.example.com",      // cleartext to a remote -> bearer exfil
		"http://api.github.com",        // cleartext downgrade of canonical host
		"https://user:pass@gh.example", // embedded credentials
		"ftp://gh.example",             // non-http scheme
		"gh.example.com",               // no scheme/host
		"://broken",                    // unparseable
	}
	for _, base := range invalid {
		t.Run("invalid/"+base, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			t.Setenv("GH_API_BASE", base)
			_, err := Load()
			require.Error(t, err, "GH_API_BASE %q must be rejected", base)
		})
	}
}

func TestLoad_OverridesViaEnv(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("PORT", "9090")
	t.Setenv("R2_BUCKET", "test-bucket")
	t.Setenv("GH_ORG", "ExampleOrg")
	t.Setenv("GH_API_BASE", "https://gh.example.test")
	t.Setenv("GH_MEMBERSHIP_CACHE_TTL", "60")
	t.Setenv("JWT_TTL_SECONDS", "300")
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "<site>/prod")
	t.Setenv("ALIAS_PREVIEW_KEY_FORMAT", "<site>/staging")
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site>/d/<ts>-<sha>/")
	t.Setenv("UPLOAD_MAX_BYTES", "5242880") // 5 MiB
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("REGISTRY_AUTHZ_TEAM", "platform")
	t.Setenv("VALKEY_PASSWORD", "secret-pw")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "test-bucket", cfg.R2.Bucket)
	assert.Equal(t, "ExampleOrg", cfg.GitHub.Org)
	assert.Equal(t, "https://gh.example.test", cfg.GitHub.APIBase)
	assert.Equal(t, 60*time.Second, cfg.GitHub.MembershipCacheTTL)
	assert.Equal(t, 5*time.Minute, cfg.JWT.TTL)
	assert.Equal(t, "<site>/prod", cfg.Aliases.ProductionKeyFormat)
	assert.Equal(t, "<site>/staging", cfg.Aliases.PreviewKeyFormat)
	assert.Equal(t, "<site>/d/<ts>-<sha>/", cfg.DeployPrefixFormat)
	assert.EqualValues(t, 5*1024*1024, cfg.UploadMaxBytes)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "platform", cfg.Registry.AuthzTeam)
	assert.Equal(t, "secret-pw", cfg.Registry.Valkey.Password)
}

func TestConfigLoad(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.GCEnabled(), "no DATABASE_URL -> GC disabled")
	assert.Equal(t, 7, cfg.Cleanup.RetentionDays)
	assert.Equal(t, 3, cfg.Cleanup.RecentKeep)
	assert.Equal(t, time.Hour, cfg.Cleanup.Grace)
	assert.Equal(t, 0, cfg.Cleanup.BlastCap)
	assert.Equal(t, "_trash/", cfg.Cleanup.TrashPrefix)
	assert.Equal(t, 7, cfg.Cleanup.RecoveryDays)
	assert.False(t, cfg.Cleanup.DryRun)
	assert.Equal(t, 15*time.Second, cfg.Cleanup.ServeCacheTTL)
}

func TestConfigLoad_Overrides(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("DATABASE_URL", "postgres://artemis@pg/artemis")
	t.Setenv("HATCHET_CLIENT_TOKEN", "ht-token")
	t.Setenv("HATCHET_ADDR", "hatchet.svc:7077")
	t.Setenv("CLEANUP_RETENTION_DAYS", "14")
	t.Setenv("CLEANUP_RECENT_KEEP", "5")
	t.Setenv("CLEANUP_GRACE", "2h")
	t.Setenv("CLEANUP_BLAST_CAP", "100")
	t.Setenv("CLEANUP_TRASH_PREFIX", "_graveyard")
	t.Setenv("CLEANUP_RECOVERY_DAYS", "30")
	t.Setenv("CLEANUP_DRY_RUN", "true")

	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.GCEnabled())
	assert.Equal(t, "postgres://artemis@pg/artemis", cfg.DatabaseURL)
	assert.Equal(t, "ht-token", cfg.Hatchet.ClientToken)
	assert.Equal(t, "hatchet.svc:7077", cfg.Hatchet.Addr)
	assert.Equal(t, 14, cfg.Cleanup.RetentionDays)
	assert.Equal(t, 5, cfg.Cleanup.RecentKeep)
	assert.Equal(t, 2*time.Hour, cfg.Cleanup.Grace)
	assert.Equal(t, 100, cfg.Cleanup.BlastCap)
	assert.Equal(t, "_graveyard/", cfg.Cleanup.TrashPrefix, "trailing slash normalized in")
	assert.Equal(t, 30, cfg.Cleanup.RecoveryDays)
	assert.True(t, cfg.Cleanup.DryRun)
}

func TestConfigLoad_GraceBelowJWTTTLFails(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("JWT_TTL_SECONDS", "3600")
	t.Setenv("CLEANUP_GRACE", "30m")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLEANUP_GRACE")
}

func TestConfigLoad_GraceBelowServeCacheTTLFails(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("JWT_TTL_SECONDS", "5")
	t.Setenv("CLEANUP_GRACE", "10s")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serve-cache")
}

func TestLoad_UploadMaxBytes_RejectsNonPositive(t *testing.T) {
	for _, bad := range []string{"0", "-1", "not-a-number", ""} {
		t.Run("v="+bad, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			t.Setenv("UPLOAD_MAX_BYTES", bad)
			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "UPLOAD_MAX_BYTES")
		})
	}
}

func TestLoad_MissingRequiredFails(t *testing.T) {
	cases := []string{
		"R2_ENDPOINT",
		"R2_ACCESS_KEY_ID",
		"R2_SECRET_ACCESS_KEY",
		"GH_CLIENT_ID",
		"JWT_SIGNING_KEY",
		"VALKEY_ADDR",
	}
	for _, omitted := range cases {
		t.Run("missing "+omitted, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			require.NoError(t, os.Unsetenv(omitted))
			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), omitted)
		})
	}
}

func TestLoad_RejectsInvalidNumeric(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("PORT", "not-a-port")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PORT")
}

func TestLoad_RejectsShortSigningKey(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("JWT_SIGNING_KEY", "tooshort")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SIGNING_KEY")
}

func TestLoad_LogLevelValidation(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("LOG_LEVEL", "absurd")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOG_LEVEL")
}

// TestLoad_RejectsMalformedDeployPrefix — B8: DEPLOY_PREFIX_FORMAT must
// contain both `<site>` and `<ts>-<sha>` tokens. Operator typos (or env
// substitution accidents) must fail-fast at boot, not surface later as
// broken R2 keys on the first deploy attempt.
func TestLoad_RejectsMalformedDeployPrefix(t *testing.T) {
	cases := []struct {
		name    string
		fmt     string
		wantSub []string
	}{
		{"missing both", "hello/", []string{"DEPLOY_PREFIX_FORMAT", "<site>", "<ts>-<sha>"}},
		{"missing site", "deploys/<ts>-<sha>/", []string{"DEPLOY_PREFIX_FORMAT", "<site>"}},
		{"missing ts-sha", "<site>/deploys/<id>/", []string{"DEPLOY_PREFIX_FORMAT", "<ts>-<sha>"}},
		{"only ts no sha", "<site>/deploys/<ts>/", []string{"DEPLOY_PREFIX_FORMAT", "<ts>-<sha>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			t.Setenv("DEPLOY_PREFIX_FORMAT", tc.fmt)
			_, err := Load()
			require.Error(t, err)
			for _, sub := range tc.wantSub {
				assert.Contains(t, err.Error(), sub)
			}
		})
	}
}

func TestLoad_AcceptsValidDeployPrefix(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site>/custom/<ts>-<sha>/sub/")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "<site>/custom/<ts>-<sha>/sub/", cfg.DeployPrefixFormat)
}

func TestLoad_RegistryAuthzTeamRejectsWhitespace(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("REGISTRY_AUTHZ_TEAM", "  ")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REGISTRY_AUTHZ_TEAM")
}

func TestValidate_RegistryAuthzTeamRejectsBlank(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, "staff", cfg.Registry.AuthzTeam)

	cfg.Registry.AuthzTeam = ""
	err = cfg.validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REGISTRY_AUTHZ_TEAM")
}

// captureSlog redirects slog.Default() to a buffer for the duration of
// the test and restores the previous default on cleanup.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestLoad_GHAPIBaseDefaultNoWarn pins the negative case: the default
// GH_API_BASE must NOT emit the override-warn. Without this, a future
// refactor could fire the warn unconditionally and bury real overrides
// in the noise.
func TestLoad_GHAPIBaseDefaultNoWarn(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	logs := captureSlog(t)
	_, err := Load()
	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "GH_API_BASE overridden")
}

// TestLoad_GHAPIBaseOverrideWarn asserts that a non-default
// GH_API_BASE triggers a startup warn carrying the configured value +
// the canonical default. The warn is the operator's only visible
// signal that GitHub probes are routing through a non-canonical host.
func TestLoad_GHAPIBaseOverrideWarn(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	const override = "https://evil.example.com"
	t.Setenv("GH_API_BASE", override)

	logs := captureSlog(t)
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, override, cfg.GitHub.APIBase)

	out := logs.String()
	assert.True(t, strings.Contains(out, "GH_API_BASE overridden"),
		"warn missing from slog output: %q", out)
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, override)
	assert.Contains(t, out, defaultGitHubAPIBase)
}

func TestLoad_RejectsNonNumericAppIDs(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("GH_APP_ID", "3.287718e+06")
	t.Setenv("GH_APP_INSTALLATION_ID", "121700722")
	t.Setenv("GH_APP_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GH_APP_ID")
}

func TestLoad_RejectsNonNumericInstallationID(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("GH_APP_ID", "3287718")
	t.Setenv("GH_APP_INSTALLATION_ID", "1.21700722e+08")
	t.Setenv("GH_APP_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GH_APP_INSTALLATION_ID")
}

func TestLoad_AcceptsNumericAppIDs(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("GH_APP_ID", "3287718")
	t.Setenv("GH_APP_INSTALLATION_ID", "121700722")
	t.Setenv("GH_APP_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "3287718", cfg.Repo.App.AppID)
	assert.Equal(t, "121700722", cfg.Repo.App.InstallationID)
}
