package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func healthySweep() sweepResult {
	return sweepResult{
		Reports: []siteDrift{{Site: "www.freecode.camp"}},
		Stats:   sweepStats{Sites: 1, IndexedTotal: 3, R2Objects: 9, PGDeploys: 3},
	}
}

func TestClassifyDrift_SaysNothingWhenTheStoresAgree(t *testing.T) {
	v := classifyDrift(healthySweep())

	assert.Empty(t, v.Op, "a clean sweep must not page anyone; the cron monitor already proves the job ran")
	assert.False(t, v.Fails)
}

func TestClassifyDrift_StaysSilentOnReclaimableDriftAlone(t *testing.T) {
	res := healthySweep()
	res.Reports[0].Tombstone = []string{"a", "b", "c"}
	res.Reports[0].Reindex = []string{"d"}

	v := classifyDrift(res)

	assert.Empty(t, v.Op,
		"orphan bytes and lost index rows waste storage but break nothing, and production carries "+
			"37 of them today: alerting on the steady state trains the operator to ignore the alert")
}

func TestClassifyDrift_AlertsOnAnAliasPointingAtNothing(t *testing.T) {
	res := healthySweep()
	res.Reports[0].Aliased = []string{"20260101-000000-abc1234"}

	v := classifyDrift(res)

	require.Equal(t, opDriftAliasedMissing, v.Op,
		"an alias whose deploy is gone is the class that breaks a live site")
	require.Error(t, v.Err)
	assert.Contains(t, v.Err.Error(), "www.freecode.camp")
	assert.False(t, v.Fails,
		"the job did its job: finding a problem is a green check-in with an event, not a failed run")
}

func TestClassifyDrift_OrphanAliasesPageWithoutFailingTheCron(t *testing.T) {
	res := healthySweep()
	res.OrphanAliases = []orphanAlias{{Dirname: "ghost.freecode.camp", Modes: []string{"production"}}}

	v := classifyDrift(res)

	require.Equal(t, opDriftOrphanAliases, v.Op,
		"an alias with no registry row is a site nobody owns still serving the public internet")
	require.Error(t, v.Err)
	assert.Contains(t, v.Err.Error(), "ghost.freecode.camp")
	assert.False(t, v.Fails,
		"only a human can clear this; a failing cron would re-page on every retry")
}

func TestClassifyDrift_AnUnreadableOrphanScanIsUnknownDriftNotZero(t *testing.T) {
	res := healthySweep()
	res.OrphanErr = errors.New("list bucket sites: connection refused")

	v := classifyDrift(res)

	require.Equal(t, opDriftUnreadable, v.Op)
	assert.True(t, v.Fails,
		"a scan that could not run proves nothing about whether a deregistered site is serving")
	assert.Contains(t, v.Err.Error(), "connection refused")
}

func TestClassifyDrift_AliasedMissingOutranksOrphanAliases(t *testing.T) {
	res := healthySweep()
	res.Reports[0].Aliased = []string{"20260101-000000-abc1234"}
	res.OrphanAliases = []orphanAlias{{Dirname: "ghost.freecode.camp", Modes: []string{"production"}}}

	v := classifyDrift(res)

	require.Equal(t, opDriftAliasedMissing, v.Op,
		"a live site serving nothing outranks a dead site serving something")
}

func TestClassifyDrift_FailsTheRunWhenASiteCannotBeRead(t *testing.T) {
	res := healthySweep()
	res.Reports[0].FailedWith = errors.New("r2: connection refused")
	res.Stats.ReadFailures = 1

	v := classifyDrift(res)

	require.Equal(t, opDriftUnreadable, v.Op)
	assert.True(t, v.Fails,
		"a site the sweep could not read is unknown drift, never zero drift, so the run must go red")
}

func TestClassifyDrift_StillPagesForAnAliasFoundBeforeAReadFailed(t *testing.T) {
	res := healthySweep()
	res.Reports[0].Aliased = []string{"20260101-000000-abc1234"}
	res.Reports = append(res.Reports, siteDrift{
		Site:       "learn.freecode.camp",
		FailedWith: errors.New("r2: connection refused"),
	})
	res.Stats.Sites = 2
	res.Stats.ReadFailures = 1

	v := classifyDrift(res)

	assert.Equal(t, opDriftAliasedMissing, v.Op,
		"drift found before a failure must still page, or a failing sweep hides the one class that breaks a site")
	assert.True(t, v.Fails,
		"the unread site is still unknown drift, so the run goes red even though the alias finding is what pages")
	assert.Contains(t, v.Err.Error(), "learn.freecode.camp",
		"this error is the whole Sentry payload, so an unread site named nowhere in it is a site the operator never learns to check")
}

func TestClassifyDrift_FailsTheRunWhenTheSweepDisagreesWithPostgres(t *testing.T) {
	res := healthySweep()
	res.Stats.PGDeploys = 0
	res.Stats.IndexedTotal = 198

	v := classifyDrift(res)

	require.Equal(t, opDriftSelfCheck, v.Op,
		"a sweep whose site list matches no index row is the original keyspace bug returning")
	assert.True(t, v.Fails)
	assert.Contains(t, v.Err.Error(), "site list is wrong")
}

func TestClassifyDrift_SelfCheckStillNamesTheUnreadSites(t *testing.T) {
	res := healthySweep()
	res.Stats.PGDeploys = 4
	res.Stats.Prunes = 3
	res.Reports = append(res.Reports, siteDrift{
		Site:       "learn.freecode.camp",
		FailedWith: errors.New("r2: connection refused"),
	})
	res.Stats.Sites = 2
	res.Stats.ReadFailures = 1

	v := classifyDrift(res)

	require.Equal(t, opDriftSelfCheck, v.Op)
	assert.Contains(t, v.Err.Error(), "learn.freecode.camp",
		"the majority-prune check is the one self-check a read failure can reach, so its error must "+
			"carry the unread sites too or the operator sees a refusal with no site to look at")
}

func TestClassifyDrift_PrefersTheSelfCheckOverAnAliasFinding(t *testing.T) {
	res := healthySweep()
	res.Stats.PGDeploys = 0
	res.Stats.IndexedTotal = 198
	res.Reports[0].Aliased = []string{"x"}

	v := classifyDrift(res)

	assert.Equal(t, opDriftSelfCheck, v.Op,
		"drift counted against a keyspace the sweep read wrongly is not evidence of drift")
}

func TestAlertOnDrift_CapturesTheVerdictAndKeepsTheRunGreen(t *testing.T) {
	var gotOp string
	var gotErr error
	restore := captureBackground
	captureBackground = func(op string, err error) { gotOp, gotErr = op, err }
	t.Cleanup(func() { captureBackground = restore })

	res := healthySweep()
	res.Reports[0].Aliased = []string{"20260101-000000-abc1234"}

	err := alertOnDrift(context.Background(), res)

	require.NoError(t, err)
	assert.Equal(t, opDriftAliasedMissing, gotOp)
	require.Error(t, gotErr)
}

func TestAlertOnDrift_ReturnsTheErrorSoTheCheckInGoesRed(t *testing.T) {
	var gotOp string
	restore := captureBackground
	captureBackground = func(op string, _ error) { gotOp = op }
	t.Cleanup(func() { captureBackground = restore })

	res := healthySweep()
	res.Reports[0].FailedWith = errors.New("r2: connection refused")
	res.Stats.ReadFailures = 1

	err := alertOnDrift(context.Background(), res)

	require.Error(t, err)
	assert.Equal(t, opDriftUnreadable, gotOp)
}

func TestAlertOnDrift_SendsNothingOnACleanSweep(t *testing.T) {
	called := false
	restore := captureBackground
	captureBackground = func(string, error) { called = true }
	t.Cleanup(func() { captureBackground = restore })

	require.NoError(t, alertOnDrift(context.Background(), healthySweep()))

	assert.False(t, called, "silence is the contract when there is nothing for a person to do")
}
