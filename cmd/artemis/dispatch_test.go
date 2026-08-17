package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDispatchSubcommand_NoArgsRunsTheServer(t *testing.T) {
	t.Parallel()

	handled, err := dispatchSubcommand(context.Background(), &bytes.Buffer{}, nil)
	require.NoError(t, err)
	require.False(t, handled, "an argv-less invocation is the server, not a subcommand")
}

func TestDispatchSubcommand_RejectsAnUnknownSubcommand(t *testing.T) {
	t.Parallel()

	handled, err := dispatchSubcommand(context.Background(), &bytes.Buffer{}, []string{"drift-report"})
	require.True(t, handled,
		"an unrecognised subcommand must not fall through to the server: a typo would boot a second "+
			"artemis instead of reporting the typo")
	require.ErrorContains(t, err, "drift-report")
}

func TestDispatchSubcommand_RejectsArgumentsToDriftReport(t *testing.T) {
	t.Parallel()

	handled, err := dispatchSubcommand(context.Background(), &bytes.Buffer{}, []string{driftReportCommand, "www"})
	require.True(t, handled)
	require.ErrorContains(t, err, "takes no arguments",
		"driftreport swept every site regardless of argv, so a site name an operator typed was silently "+
			"ignored and the whole-fleet report read as scoped")
}
