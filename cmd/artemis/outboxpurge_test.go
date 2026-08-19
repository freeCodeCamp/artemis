package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

type fakeOutboxPurger struct {
	calls  int
	before time.Time
	limit  int
	n      int
	err    error
}

func (f *fakeOutboxPurger) PurgeOutbox(_ context.Context, before time.Time, limit int) (int, error) {
	f.calls++
	f.before = before
	f.limit = limit
	return f.n, f.err
}

func purgeWiring(ob *fakeOutboxPurger, retention time.Duration) *gcWiring {
	return &gcWiring{
		SiteGC:          &gc.SiteGC{},
		Reconciler:      &gc.Reconciler{},
		Purge:           &gc.TombstonePurge{Store: noTombstones{}, Now: time.Now},
		Outbox:          ob,
		OutboxRetention: retention,
	}
}

type noTombstones struct{ gc.TombstoneReaper }

func (noTombstones) ExpiredTombstones(context.Context, time.Time) ([]gc.Tombstone, error) {
	return nil, nil
}

func TestTombstonePurgeWorkflow_AlsoRetiresPublishedOutboxRows(t *testing.T) {
	ob := &fakeOutboxPurger{}
	def := defByName(t, gcWorkflowDefs(purgeWiring(ob, 30*24*time.Hour), false, cleanSweep),
		worker.WorkflowTombstonePurge)

	require.NoError(t, def.Handler(context.Background(), nil))

	require.Equal(t, 1, ob.calls, "the nightly purge is the only job that bounds outbox growth")
	assert.WithinDuration(t, time.Now().UTC().Add(-30*24*time.Hour), ob.before, time.Minute,
		"the cutoff is now minus the configured retention")
	assert.Equal(t, outboxPurgeBatch, ob.limit,
		"a bounded batch keeps one night's delete off a long transaction")
}

func TestTombstonePurgeWorkflow_LeavesTheOutboxAloneOnADryRun(t *testing.T) {
	ob := &fakeOutboxPurger{}
	def := defByName(t, gcWorkflowDefs(purgeWiring(ob, 30*24*time.Hour), true, cleanSweep),
		worker.WorkflowTombstonePurge)

	require.NoError(t, def.Handler(context.Background(), nil))

	assert.Zero(t, ob.calls,
		"CLEANUP_DRY_RUN means plan, never delete — it must cover every destructive job in the run, "+
			"not only the tombstone half")
}

func TestTombstonePurgeWorkflow_FailsWhenTheOutboxPurgeFails(t *testing.T) {
	ob := &fakeOutboxPurger{err: errors.New("pg down")}
	def := defByName(t, gcWorkflowDefs(purgeWiring(ob, 30*24*time.Hour), false, cleanSweep),
		worker.WorkflowTombstonePurge)

	err := def.Handler(context.Background(), nil)

	require.ErrorContains(t, err, "pg down",
		"a purge that could not run must redden the cron check-in rather than report a clean night")
}

func TestTombstonePurgeWorkflow_SkipsTheOutboxWhenRetentionIsUnset(t *testing.T) {
	ob := &fakeOutboxPurger{}
	def := defByName(t, gcWorkflowDefs(purgeWiring(ob, 0), false, cleanSweep),
		worker.WorkflowTombstonePurge)

	require.NoError(t, def.Handler(context.Background(), nil))
	assert.Zero(t, ob.calls)
}

func TestNewGCWiring_CarriesTheConfiguredOutboxRetention(t *testing.T) {
	cfg := &config.Config{
		DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/",
		Cleanup:            config.CleanupConfig{TrashPrefix: "_trash/", OutboxRetentionDays: 14},
	}
	cfg.Aliases.ProductionKeyFormat = "<site>/production"
	cfg.Aliases.PreviewKeyFormat = "<site>/preview"

	w, err := newGCWiring(cfg, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, 14*24*time.Hour, w.OutboxRetention)
}
