// Package config loads Artemis configuration from environment variables.
//
// Required vars (no defaults — fail-fast on Load):
//
//	R2_ENDPOINT, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY,
//	GH_CLIENT_ID, JWT_SIGNING_KEY, VALKEY_ADDR
//
// All other vars have defaults documented on each field.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultGitHubAPIBase is the trusted upstream for GitHub identity +
// team-membership probes. Overridable via GH_API_BASE for testing
// against a proxy; Load() emits a warn when overridden so operators
// see the non-default in startup logs.
const defaultGitHubAPIBase = "https://api.github.com"

// Config holds the full Artemis runtime configuration.
type Config struct {
	Port               int
	R2                 R2Config
	GitHub             GitHubConfig
	JWT                JWTConfig
	Aliases            AliasConfig
	DeployPrefixFormat string
	UploadMaxBytes     int64 // single PUT /upload body cap; default 100 MiB
	LogLevel           string
	Registry           RegistryConfig
}

// R2Config holds the Cloudflare R2 (S3-compatible) credentials and target bucket.
type R2Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
}

// GitHubConfig holds GitHub OAuth + REST settings used by the auth plane.
type GitHubConfig struct {
	ClientID           string
	Org                string
	APIBase            string
	MembershipCacheTTL time.Duration
}

// JWTConfig holds the deploy-session JWT signing key + TTL (HS256).
type JWTConfig struct {
	SigningKey string
	TTL        time.Duration
}

// AliasConfig holds R2 alias key formats. The literal `<site>` token is
// substituted at write-time per request.
type AliasConfig struct {
	ProductionKeyFormat string
	PreviewKeyFormat    string
}

// RegistryConfig holds the Valkey-backed registry settings: connection
// to the KV store + the GitHub team that gates state-mutating registry
// endpoints (POST /api/site/register, PATCH, DELETE).
type RegistryConfig struct {
	// AuthzTeam is the GitHub team slug that gates state-mutating
	// /api/site/* endpoints. Default "staff".
	AuthzTeam string

	// Valkey connection details.
	Valkey ValkeyConfig
}

// ValkeyConfig holds the connection string + auth password for the
// Valkey instance backing the registry. Address follows host:port
// (no scheme). Password is required by the production chart but
// dev / unauthenticated instances may set it to the empty string.
type ValkeyConfig struct {
	Addr     string
	Password string
}

const (
	minSigningKeyBytes       = 32
	defaultRegistryAuthzTeam = "staff"
)

var validLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

// Load reads environment variables, applies defaults, validates required
// fields, and returns a populated Config. It returns an error naming the
// offending variable on the first validation failure.
func Load() (*Config, error) {
	cfg := &Config{
		Port: 8080,
		R2: R2Config{
			Bucket: "universe-static-apps-01",
		},
		GitHub: GitHubConfig{
			Org:                "freeCodeCamp",
			APIBase:            defaultGitHubAPIBase,
			MembershipCacheTTL: 5 * time.Minute,
		},
		JWT: JWTConfig{
			TTL: 15 * time.Minute,
		},
		Aliases: AliasConfig{
			ProductionKeyFormat: "<site>/production",
			PreviewKeyFormat:    "<site>/preview",
		},
		DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/",
		UploadMaxBytes:     100 * 1024 * 1024, // 100 MiB
		LogLevel:           "info",
		Registry: RegistryConfig{
			AuthzTeam: defaultRegistryAuthzTeam,
		},
	}

	if v, ok := os.LookupEnv("PORT"); ok {
		port, err := strconv.Atoi(v)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid PORT %q: must be 1-65535", v)
		}
		cfg.Port = port
	}

	cfg.R2.Endpoint = getEnv("R2_ENDPOINT")
	cfg.R2.AccessKeyID = getEnv("R2_ACCESS_KEY_ID")
	cfg.R2.SecretAccessKey = getEnv("R2_SECRET_ACCESS_KEY")
	if v, ok := os.LookupEnv("R2_BUCKET"); ok && v != "" {
		cfg.R2.Bucket = v
	}

	cfg.GitHub.ClientID = getEnv("GH_CLIENT_ID")
	if v, ok := os.LookupEnv("GH_ORG"); ok && v != "" {
		cfg.GitHub.Org = v
	}
	if v, ok := os.LookupEnv("GH_API_BASE"); ok && v != "" {
		cfg.GitHub.APIBase = v
	}
	if v, ok := os.LookupEnv("GH_MEMBERSHIP_CACHE_TTL"); ok {
		ttl, err := strconv.Atoi(v)
		if err != nil || ttl <= 0 {
			return nil, fmt.Errorf("invalid GH_MEMBERSHIP_CACHE_TTL %q: must be positive integer (seconds)", v)
		}
		cfg.GitHub.MembershipCacheTTL = time.Duration(ttl) * time.Second
	}

	cfg.JWT.SigningKey = getEnv("JWT_SIGNING_KEY")
	if v, ok := os.LookupEnv("JWT_TTL_SECONDS"); ok {
		ttl, err := strconv.Atoi(v)
		if err != nil || ttl <= 0 {
			return nil, fmt.Errorf("invalid JWT_TTL_SECONDS %q: must be positive integer", v)
		}
		cfg.JWT.TTL = time.Duration(ttl) * time.Second
	}

	if v, ok := os.LookupEnv("ALIAS_PRODUCTION_KEY_FORMAT"); ok && v != "" {
		cfg.Aliases.ProductionKeyFormat = v
	}
	if v, ok := os.LookupEnv("ALIAS_PREVIEW_KEY_FORMAT"); ok && v != "" {
		cfg.Aliases.PreviewKeyFormat = v
	}
	if v, ok := os.LookupEnv("DEPLOY_PREFIX_FORMAT"); ok && v != "" {
		cfg.DeployPrefixFormat = v
	}
	if v, ok := os.LookupEnv("UPLOAD_MAX_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid UPLOAD_MAX_BYTES %q: must be positive integer (bytes)", v)
		}
		cfg.UploadMaxBytes = n
	}

	if v, ok := os.LookupEnv("LOG_LEVEL"); ok && v != "" {
		cfg.LogLevel = v
	}

	if v, ok := os.LookupEnv("REGISTRY_AUTHZ_TEAM"); ok && v != "" {
		cfg.Registry.AuthzTeam = v
	}
	cfg.Registry.Valkey.Addr = getEnv("VALKEY_ADDR")
	if v, ok := os.LookupEnv("VALKEY_PASSWORD"); ok && v != "" {
		cfg.Registry.Valkey.Password = v
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// Non-default GH_API_BASE means every GitHub identity + team-
	// membership probe routes through a non-canonical host. Legit for
	// testing against a recording proxy or a GitHub Enterprise
	// instance; suspicious if it appears in a production pod env.
	// Warn loudly so operators see the override in startup logs.
	if cfg.GitHub.APIBase != defaultGitHubAPIBase {
		slog.Warn("GH_API_BASE overridden from default; ensure the override is trusted",
			"configured", cfg.GitHub.APIBase,
			"default", defaultGitHubAPIBase,
		)
	}

	return cfg, nil
}

func (c *Config) validate() error {
	missing := func(name string) error {
		return fmt.Errorf("required env var %s is not set", name)
	}

	if c.R2.Endpoint == "" {
		return missing("R2_ENDPOINT")
	}
	if c.R2.AccessKeyID == "" {
		return missing("R2_ACCESS_KEY_ID")
	}
	if c.R2.SecretAccessKey == "" {
		return missing("R2_SECRET_ACCESS_KEY")
	}
	if c.GitHub.ClientID == "" {
		return missing("GH_CLIENT_ID")
	}
	if c.JWT.SigningKey == "" {
		return missing("JWT_SIGNING_KEY")
	}
	if len(c.JWT.SigningKey) < minSigningKeyBytes {
		return fmt.Errorf("JWT_SIGNING_KEY must be at least %d bytes (got %d)", minSigningKeyBytes, len(c.JWT.SigningKey))
	}
	if _, ok := validLogLevels[c.LogLevel]; !ok {
		return fmt.Errorf("invalid LOG_LEVEL %q: must be one of debug, info, warn, error", c.LogLevel)
	}
	if err := validateDeployPrefixFormat(c.DeployPrefixFormat); err != nil {
		return err
	}
	if c.Registry.Valkey.Addr == "" {
		return missing("VALKEY_ADDR")
	}
	if c.Registry.AuthzTeam == "" {
		return fmt.Errorf("REGISTRY_AUTHZ_TEAM must not be empty")
	}
	return nil
}

// validateDeployPrefixFormat asserts the deploy-key template contains
// both required placeholders. Both must be present so the per-deploy
// prefix is unambiguous and the site-prefix can be derived for listing.
func validateDeployPrefixFormat(fmtStr string) error {
	required := []string{"<site>", "<ts>-<sha>"}
	var missing []string
	for _, tok := range required {
		if !strings.Contains(fmtStr, tok) {
			missing = append(missing, tok)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("invalid DEPLOY_PREFIX_FORMAT %q: must contain %s",
			fmtStr, strings.Join(missing, " and "))
	}
	return nil
}

// getEnv returns the env var value or empty string. validate() then
// surfaces any missing required vars with a uniform error message;
// using empty string here lets validate() be the single source of
// truth for "missing var" errors rather than scattering os.Getenv
// checks.
func getEnv(name string) string {
	return os.Getenv(name)
}
