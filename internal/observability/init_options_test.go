package observability

import (
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInit_ClientOptionsHardening(t *testing.T) {
	t.Setenv("HOSTNAME", "artemis-pod-xyz")
	opts := buildClientOptions(Config{DSN: "https://public@example.test/1", Environment: "prod", Release: "r1"})

	assert.Equal(t, "artemis-pod-xyz", opts.ServerName, "ServerName from pod hostname")
	assert.Equal(t, maxBreadcrumbs, opts.MaxBreadcrumbs, "explicit MaxBreadcrumbs")
	require.NotNil(t, opts.BeforeBreadcrumb, "BeforeBreadcrumb hook installed")

	bc := opts.BeforeBreadcrumb(&sentry.Breadcrumb{Message: "auth Bearer abc123def"}, nil)
	require.NotNil(t, bc)
	assert.Contains(t, bc.Message, "[REDACTED]", "breadcrumb secrets scrubbed at add time")
}
