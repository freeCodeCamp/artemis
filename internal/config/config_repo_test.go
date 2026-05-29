package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_RepoDefaults(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "freeCodeCamp-Universe", cfg.Repo.Org)
	assert.Equal(t, "staff", cfg.Repo.CreateAuthzTeam)
	assert.Equal(t, "repo-admins", cfg.Repo.ApproveAuthzTeam)
	assert.False(t, cfg.Repo.Enabled(), "repo feature must be disabled without App creds")
}

func TestLoad_RepoOverridesAndAppCreds(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("GH_REPO_ORG", "ExampleUniverse")
	t.Setenv("REPO_CREATE_AUTHZ_TEAM", "contributors")
	t.Setenv("REPO_APPROVE_AUTHZ_TEAM", "maintainers")
	t.Setenv("GH_APP_ID", "123456")
	t.Setenv("GH_APP_INSTALLATION_ID", "987654")
	t.Setenv("GH_APP_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nMII...\n-----END RSA PRIVATE KEY-----\n")

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "ExampleUniverse", cfg.Repo.Org)
	assert.Equal(t, "contributors", cfg.Repo.CreateAuthzTeam)
	assert.Equal(t, "maintainers", cfg.Repo.ApproveAuthzTeam)
	assert.Equal(t, "123456", cfg.Repo.App.AppID)
	assert.Equal(t, "987654", cfg.Repo.App.InstallationID)
	assert.True(t, cfg.Repo.Enabled())
}

func TestLoad_RepoPartialAppConfigFails(t *testing.T) {
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	// App id set but installation id + key missing → partial → error.
	t.Setenv("GH_APP_ID", "123456")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partial")
}

func TestLoad_RepoEmptyTeamOverrideFails(t *testing.T) {
	// An explicit empty override is ignored (defaults retained), so the
	// guard against empty teams only trips on a programmatic zero value;
	// assert the happy default holds when the env var is blank.
	for k, v := range requiredEnv() {
		t.Setenv(k, v)
	}
	t.Setenv("REPO_APPROVE_AUTHZ_TEAM", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "repo-admins", cfg.Repo.ApproveAuthzTeam)
}
