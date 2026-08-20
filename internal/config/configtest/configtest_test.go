package configtest_test

import (
	"os"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/config/configtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestHermetic_LeavesUndeclaredVariablesAbsentNotEmpty(t *testing.T) {
	t.Setenv("PORT", "9999")

	configtest.Hermetic(t, config.EnvKeys(), requiredEnv())

	_, present := os.LookupEnv("PORT")
	require.False(t, present,
		"PORT is read without an empty-string guard, so an empty value would reach strconv and fail Load")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 8080, cfg.Port, "an undeclared variable must fall back to the default")
}

func TestHermetic_OverridesAnAmbientValueItDeclares(t *testing.T) {
	t.Setenv("R2_BUCKET", "leaked-from-the-shell")

	want := requiredEnv()
	want["R2_BUCKET"] = "declared-by-the-test"
	configtest.Hermetic(t, config.EnvKeys(), want)

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "declared-by-the-test", cfg.R2.Bucket)
}

func TestHermetic_RestoresEveryClearedVariableWhenTheTestEnds(t *testing.T) {
	const ambient = "ambient-value-set-outside"
	t.Setenv("R2_BUCKET", ambient)

	t.Run("inner", func(t *testing.T) {
		configtest.Hermetic(t, config.EnvKeys(), requiredEnv())
		_, present := os.LookupEnv("R2_BUCKET")
		require.False(t, present, "the inner test must not see the ambient value")
	})

	assert.Equal(t, ambient, os.Getenv("R2_BUCKET"),
		"clearing must be scoped to the test; leaking it breaks every later test in the binary")
}

func TestUnreadableKeys_NamesAKeyLoadNeverReads(t *testing.T) {
	want := requiredEnv()
	want["DEPLOY_PREFIX_FORMATT"] = "typo"

	assert.Equal(t, []string{"DEPLOY_PREFIX_FORMATT"},
		configtest.UnreadableKeys(config.EnvKeys(), want),
		"a typo sets a variable nobody reads, so the test passes while asserting nothing")
}

func TestUnreadableKeys_AcceptsEveryKeyLoadReads(t *testing.T) {
	all := map[string]string{}
	for _, k := range config.EnvKeys() {
		all[k] = "x"
	}

	assert.Empty(t, configtest.UnreadableKeys(config.EnvKeys(), all))
}
