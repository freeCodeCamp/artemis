package config

import (
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

	assert.Equal(t, "/etc/artemis/sites.yaml", cfg.SitesYAMLPath)

	assert.Equal(t, "0123456789abcdef0123456789abcdef", cfg.JWT.SigningKey)
	assert.Equal(t, 15*time.Minute, cfg.JWT.TTL)

	assert.Equal(t, "<site>/production", cfg.Aliases.ProductionKeyFormat)
	assert.Equal(t, "<site>/preview", cfg.Aliases.PreviewKeyFormat)
	assert.Equal(t, "<site>/deploys/<ts>-<sha>/", cfg.DeployPrefixFormat)
	assert.EqualValues(t, 100*1024*1024, cfg.UploadMaxBytes)

	assert.Equal(t, "info", cfg.LogLevel)
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
	t.Setenv("SITES_YAML_PATH", "/tmp/sites.yaml")
	t.Setenv("JWT_TTL_SECONDS", "300")
	t.Setenv("ALIAS_PRODUCTION_KEY_FORMAT", "<site>/prod")
	t.Setenv("ALIAS_PREVIEW_KEY_FORMAT", "<site>/staging")
	t.Setenv("DEPLOY_PREFIX_FORMAT", "<site>/d/<ts>-<sha>/")
	t.Setenv("UPLOAD_MAX_BYTES", "5242880") // 5 MiB
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Port)
	assert.Equal(t, "test-bucket", cfg.R2.Bucket)
	assert.Equal(t, "ExampleOrg", cfg.GitHub.Org)
	assert.Equal(t, "https://gh.example.test", cfg.GitHub.APIBase)
	assert.Equal(t, 60*time.Second, cfg.GitHub.MembershipCacheTTL)
	assert.Equal(t, "/tmp/sites.yaml", cfg.SitesYAMLPath)
	assert.Equal(t, 5*time.Minute, cfg.JWT.TTL)
	assert.Equal(t, "<site>/prod", cfg.Aliases.ProductionKeyFormat)
	assert.Equal(t, "<site>/staging", cfg.Aliases.PreviewKeyFormat)
	assert.Equal(t, "<site>/d/<ts>-<sha>/", cfg.DeployPrefixFormat)
	assert.EqualValues(t, 5*1024*1024, cfg.UploadMaxBytes)
	assert.Equal(t, "debug", cfg.LogLevel)
}

// TestLoad_UploadMaxBytes_RejectsNonPositive — env var is additive but
// when set must be a positive integer. Empty/absent → default; explicit
// "0" or negative → boot-time error.
func TestLoad_UploadMaxBytes_RejectsNonPositive(t *testing.T) {
	for _, bad := range []string{"0", "-1", "not-a-number", ""} {
		t.Run("v="+bad, func(t *testing.T) {
			for k, v := range requiredEnv() {
				t.Setenv(k, v)
			}
			t.Setenv("UPLOAD_MAX_BYTES", bad)
			_, err := Load()
			if bad == "" {
				// empty is treated as set-but-blank; ParseInt on "" → error
				require.Error(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "UPLOAD_MAX_BYTES")
		})
	}
}

func TestLoad_MissingRequiredFails(t *testing.T) {
	t.Run("missing R2_ENDPOINT", func(t *testing.T) {
		for k, v := range requiredEnv() {
			if k == "R2_ENDPOINT" {
				continue
			}
			t.Setenv(k, v)
		}
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "R2_ENDPOINT")
	})

	t.Run("missing JWT_SIGNING_KEY", func(t *testing.T) {
		for k, v := range requiredEnv() {
			if k == "JWT_SIGNING_KEY" {
				continue
			}
			t.Setenv(k, v)
		}
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWT_SIGNING_KEY")
	})
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
