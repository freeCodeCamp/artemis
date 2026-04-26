package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample coverage.txt produced by `go tool cover -func`.
const sampleCov = `github.com/freeCodeCamp/artemis/internal/auth/github.go:55:	NewGitHubClient	100.0%
github.com/freeCodeCamp/artemis/internal/auth/github.go:81:	ValidateToken	75.0%
github.com/freeCodeCamp/artemis/internal/auth/jwt.go:24:	NewSigner	95.0%
github.com/freeCodeCamp/artemis/internal/handler/deploy.go:35:	DeployInit	81.0%
github.com/freeCodeCamp/artemis/internal/handler/deploy.go:83:	DeployUpload	79.0%
github.com/freeCodeCamp/artemis/internal/handler/deploy.go:130:	DeployFinalize	83.0%
github.com/freeCodeCamp/artemis/internal/r2/r2.go:20:	New	60.0%
total:	(statements)	82.5%
`

func TestParseCoverage_AggregatesPerPkg(t *testing.T) {
	pcts, err := parseCoverage(strings.NewReader(sampleCov))
	require.NoError(t, err)

	// auth pkg: avg(100 + 75 + 95) / 3 = 90.0
	assert.InDelta(t, 90.0, pcts["github.com/freeCodeCamp/artemis/internal/auth"], 0.01)
	// handler pkg: avg(81 + 79 + 83) / 3 = 81.0
	assert.InDelta(t, 81.0, pcts["github.com/freeCodeCamp/artemis/internal/handler"], 0.01)
	// r2 pkg: 60.0
	assert.InDelta(t, 60.0, pcts["github.com/freeCodeCamp/artemis/internal/r2"], 0.01)
}

func TestCheckThreshold_AllAbove_NoError(t *testing.T) {
	pcts, err := parseCoverage(strings.NewReader(sampleCov))
	require.NoError(t, err)

	failed, err := checkThreshold(pcts, 80.0, []string{
		"github.com/freeCodeCamp/artemis/internal/auth",
		"github.com/freeCodeCamp/artemis/internal/handler",
	})
	require.NoError(t, err)
	assert.Empty(t, failed)
}

func TestCheckThreshold_BelowFloor_Reports(t *testing.T) {
	pcts, err := parseCoverage(strings.NewReader(sampleCov))
	require.NoError(t, err)

	failed, err := checkThreshold(pcts, 80.0, []string{
		"github.com/freeCodeCamp/artemis/internal/r2", // 60% — below
	})
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.Equal(t, "github.com/freeCodeCamp/artemis/internal/r2", failed[0].Pkg)
	assert.InDelta(t, 60.0, failed[0].Got, 0.01)
}

func TestCheckThreshold_MissingPkg_IsError(t *testing.T) {
	pcts, err := parseCoverage(strings.NewReader(sampleCov))
	require.NoError(t, err)

	_, err = checkThreshold(pcts, 80.0, []string{
		"github.com/freeCodeCamp/artemis/internal/missing",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func TestParseCoverage_IgnoresTotalAndBlank(t *testing.T) {
	in := `

github.com/freeCodeCamp/artemis/internal/auth/jwt.go:5:	Sign	77.7%
total:	(statements)	77.7%
`
	pcts, err := parseCoverage(strings.NewReader(in))
	require.NoError(t, err)
	assert.Len(t, pcts, 1)
	assert.InDelta(t, 77.7, pcts["github.com/freeCodeCamp/artemis/internal/auth"], 0.01)
}

func TestParseCoverage_RejectsMalformed(t *testing.T) {
	in := `garbage line without tabs`
	_, err := parseCoverage(strings.NewReader(in))
	require.Error(t, err)
}
