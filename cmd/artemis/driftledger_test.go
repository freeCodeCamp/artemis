package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type recordingLedger struct {
	calls     int
	runBudget time.Duration
	overdue   time.Duration
	drift     pg.LedgerDrift
}

func (l *recordingLedger) LedgerAudit(_ context.Context, _ time.Time, runBudget, overdue time.Duration) (pg.LedgerDrift, error) {
	l.calls++
	l.runBudget, l.overdue = runBudget, overdue
	return l.drift, nil
}

func ledgerSweeper(t *testing.T, ledger ledgerAuditor) *driftSweep {
	t.Helper()
	bucket := orphanBucket{dirnames: []string{"www.freecode.camp"}, objects: map[string]bool{"www.freecode.camp/production": true}}
	repo := orphanRepo{dirnames: []sitekey.Dirname{"www.freecode.camp"}}
	reg := statefulRegistryReader{sites: []registry.Site{{Slug: "www", State: registry.StateActive}}}
	s := newOrphanSweeper(t, bucket, repo, reg)
	s.ledger = ledger
	return s
}

func TestDriftSweep_LedgerPhaseRunsOnceOnTheFleetSweep(t *testing.T) {
	t.Parallel()
	ledger := &recordingLedger{drift: pg.LedgerDrift{Stuck: []pg.StuckClaim{{Slug: "held"}}}}

	res, err := ledgerSweeper(t, ledger).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, ledger.calls)
	assert.Equal(t, gcRunBudget, ledger.runBudget, "a claim is stuck once the run that took it must have been killed")
	assert.Equal(t, ledgerOverdue, ledger.overdue)
	assert.Equal(t, ledger.drift, res.Ledger)
}

func TestDriftSweep_LedgerPhaseSkipsASingleSiteRun(t *testing.T) {
	t.Parallel()
	ledger := &recordingLedger{drift: pg.LedgerDrift{Stuck: []pg.StuckClaim{{Slug: "held"}}}}

	res, err := ledgerSweeper(t, ledger).runSite(context.Background(), "www.freecode.camp")
	require.NoError(t, err)

	assert.Equal(t, 0, ledger.calls, "artemis reconcile <site> must not report fleet-wide ledger rows")
	assert.True(t, res.Ledger.Empty())
}

func TestDriftSweep_NoLedgerMeansNoPhase(t *testing.T) {
	t.Parallel()
	res, err := ledgerSweeper(t, nil).Run(context.Background())
	require.NoError(t, err)
	assert.True(t, res.Ledger.Empty())
	assert.NoError(t, res.LedgerErr)
}

func TestLedgerOverdue_CoversAFullBatchAfterTheNextSweep(t *testing.T) {
	assert.Greater(t, ledgerOverdue, 24*time.Hour+reclaimBatchWorstCase,
		"a row emitted by tonight's sweep and still queued behind a full batch must not be reported as overdue")
}

func TestLedgerUnlessDryRun(t *testing.T) {
	assert.Nil(t, ledgerUnlessDryRun(&pg.RegistryStore{}, true), "a dry run never writes a claim, so every expired row would read as overdue")
	assert.NotNil(t, ledgerUnlessDryRun(&pg.RegistryStore{}, false))
}
