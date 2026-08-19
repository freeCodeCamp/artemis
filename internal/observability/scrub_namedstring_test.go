package observability

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedString string

func TestScrubAttrs_ScrubsANamedStringTypeLikeAPlainOne(t *testing.T) {
	secret := "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyzAB"
	plain := ScrubAttrs([]slog.Attr{slog.String("k", secret)})
	require.Len(t, plain, 1)
	require.NotContains(t, plain[0].Value.String(), secret,
		"fixture check: a plain string attr must already be scrubbed")

	named := ScrubAttrs([]slog.Attr{slog.Any("k", namedString(secret))})

	require.Len(t, named, 1)
	assert.Equal(t, plain[0].Value.String(), named[0].Value.String(),
		"a named string type reaches slog as KindAny; typed domain values must not slip past the "+
			"scrubber just because they stopped being a bare string")
}
