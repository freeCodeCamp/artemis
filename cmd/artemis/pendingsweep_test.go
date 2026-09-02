package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/gc"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

var pendingNow = time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)

type pendingSites struct {
	sites  []sitekey.Dirname
	cuts   []time.Time
	limits []int
	err    error
}

func (p *pendingSites) SitesWithExpiredPending(_ context.Context, before time.Time, limit int) ([]sitekey.Dirname, error) {
	p.cuts = append(p.cuts, before)
	p.limits = append(p.limits, limit)
	if p.err != nil {
		return nil, p.err
	}
	if len(p.sites) > limit {
		return p.sites[:limit], nil
	}
	return p.sites, nil
}

type sweepStore struct{ tombstoned []string }

func (s *sweepStore) DeploysForSite(context.Context, sitekey.Dirname) ([]gc.Deploy, error) {
	return nil, nil
}

func (s *sweepStore) AliasTargets(context.Context, sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	return map[string]struct{}{}, time.Time{}, nil
}

func (s *sweepStore) Tombstone(_ context.Context, site sitekey.Dirname, d gc.Deploy) error {
	s.tombstoned = append(s.tombstoned, string(site)+"/"+d.ID)
	return nil
}

type sweepMover struct{ moves int }

func (m *sweepMover) MovePrefix(context.Context, string, string) (int, error) {
	m.moves++
	return 1, nil
}

type sweepPending struct {
	rows map[string][]gc.Deploy
	cuts []time.Time
}

func (p *sweepPending) ExpiredPendingDeploys(_ context.Context, site sitekey.Dirname, before time.Time) ([]gc.Deploy, error) {
	p.cuts = append(p.cuts, before)
	return p.rows[string(site)], nil
}

type sweepSession struct{}

func (sweepSession) WithSiteLock(ctx context.Context, _ sitekey.Dirname, fn func(context.Context) error) error {
	return fn(ctx)
}
func (sweepSession) Close(context.Context) {}

type sweepLocker struct{}

func (sweepLocker) NewLockSession(context.Context) (gc.LockSession, error) {
	return sweepSession{}, nil
}

func newSweepGC(store gc.Store, mover gc.Mover, pending gc.PendingSource) *gc.SiteGC {
	return &gc.SiteGC{
		Store:   store,
		Mover:   mover,
		Pending: pending,
		Locker:  sweepLocker{},
		PendingIDs: func(_ context.Context, site sitekey.Dirname) (map[string]struct{}, error) {
			ids := map[string]struct{}{}
			if p, ok := pending.(*sweepPending); ok {
				for _, d := range p.rows[string(site)] {
					ids[d.ID] = struct{}{}
				}
			}
			return ids, nil
		},
		Policy:       gc.Policy{Grace: 72 * time.Hour, RecentKeep: 3},
		BlastCap:     100,
		DeployPrefix: func(s sitekey.Dirname, id string) string { return string(s) + "/deploys/" + id + "/" },
		TrashPrefix:  func(s sitekey.Dirname, id string) string { return "_trash/" + string(s) + "/" + id + "/" },
		LiveAliases: func(context.Context, sitekey.Dirname) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		},
		Now: func() time.Time { return pendingNow },
	}
}

func TestRunPendingSweep_CollectsEverySiteTheQueryReturns(t *testing.T) {
	store := &sweepStore{}
	mover := &sweepMover{}
	pending := &sweepPending{rows: map[string][]gc.Deploy{
		"latex":        {{ID: "20260826-141749-3a48d42", Mtime: pendingNow.Add(-120 * time.Hour)}},
		"plumb-select": {{ID: "20260824-083539-568adfd", Mtime: pendingNow.Add(-150 * time.Hour)}},
	}}
	src := &pendingSites{sites: []sitekey.Dirname{"latex", "plumb-select"}}

	err := runPendingSweep(context.Background(), src, newSweepGC(store, mover, pending), false)
	require.NoError(t, err)

	assert.ElementsMatch(t,
		[]string{"latex/20260826-141749-3a48d42", "plumb-select/20260824-083539-568adfd"},
		store.tombstoned,
		"B5: an abandoned deploy on an otherwise-idle site emits no site.changed event, so the nightly cron is its only collector")
	assert.Equal(t, 2, mover.moves)
}

func TestRunPendingSweep_BothCutoffsMatchTheGraceWindow(t *testing.T) {
	src := &pendingSites{sites: []sitekey.Dirname{"latex"}}
	pending := &sweepPending{rows: map[string][]gc.Deploy{
		"latex": {{ID: "abandoned", Mtime: pendingNow.Add(-120 * time.Hour)}},
	}}

	require.NoError(t, runPendingSweep(context.Background(), src,
		newSweepGC(&sweepStore{}, &sweepMover{}, pending), false))

	want := pendingNow.Add(-72 * time.Hour)
	require.Len(t, src.cuts, 1)
	require.Len(t, pending.cuts, 1, "SweepPending must actually reach its own cutoff for this test to mean anything")
	assert.Equal(t, want, src.cuts[0], "the site query picks the work list")
	assert.Equal(t, want, pending.cuts[0],
		"SweepPending recomputes the cutoff independently; a divergence makes the cron visit sites it then declines to collect")
}

func TestRunPendingSweep_StopsWhenTheRunBudgetExpires(t *testing.T) {
	store := &sweepStore{}
	pending := &sweepPending{rows: map[string][]gc.Deploy{
		"a": {{ID: "x", Mtime: pendingNow.Add(-120 * time.Hour)}},
		"b": {{ID: "y", Mtime: pendingNow.Add(-120 * time.Hour)}},
	}}
	src := &pendingSites{sites: []sitekey.Dirname{"a", "b"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runPendingSweep(ctx, src, newSweepGC(store, &sweepMover{}, pending), false)

	require.Error(t, err)
	assert.Empty(t, store.tombstoned,
		"execute runs its lock sessions and moves on context.WithoutCancel, so only an explicit ctx.Err() "+
			"check stops the loop trashing bytes past the run budget")
}

func TestRunPendingSweep_CapsTheSitesVisitedInOneNight(t *testing.T) {
	rows := map[string][]gc.Deploy{}
	var sites []sitekey.Dirname
	for i := range pendingSweepSiteLimit + 10 {
		name := sitekey.Dirname(fmt.Sprintf("site-%03d", i))
		sites = append(sites, name)
		rows[string(name)] = []gc.Deploy{{ID: "abandoned", Mtime: pendingNow.Add(-120 * time.Hour)}}
	}
	store := &sweepStore{}

	require.NoError(t, runPendingSweep(context.Background(), &pendingSites{sites: sites},
		newSweepGC(store, &sweepMover{}, &sweepPending{rows: rows}), false))

	assert.Len(t, store.tombstoned, pendingSweepSiteLimit,
		"BlastCap is per-site, so without a site ceiling one night's delete is unbounded; the backlog drains over nights")
}

func TestRunPendingSweep_NoPendingSourceWiredIsANoOp(t *testing.T) {
	src := &pendingSites{sites: []sitekey.Dirname{"latex"}}
	g := newSweepGC(&sweepStore{}, &sweepMover{}, nil)

	require.NoError(t, runPendingSweep(context.Background(), src, g, false))
	assert.Empty(t, src.cuts, "without a pending reader the sweep must not even query")
}

func TestRunPendingSweep_NoRevalidationReaderIsANoOp(t *testing.T) {
	src := &pendingSites{sites: []sitekey.Dirname{"latex"}}
	g := newSweepGC(&sweepStore{}, &sweepMover{}, &sweepPending{})
	g.PendingIDs = nil

	require.NoError(t, runPendingSweep(context.Background(), src, g, false))
	assert.Empty(t, src.cuts,
		"SweepPending refuses without the under-lock revalidation reader, so the cron must not queue work it cannot do safely")
}

func TestRunPendingSweep_QueryFailureSurfaces(t *testing.T) {
	src := &pendingSites{err: assert.AnError}

	err := runPendingSweep(context.Background(), src,
		newSweepGC(&sweepStore{}, &sweepMover{}, &sweepPending{}), false)

	require.Error(t, err, "a cron that cannot read its work list must fail loudly, not report a clean run")
}

func TestRunPendingSweep_DryRunMovesNothing(t *testing.T) {
	store := &sweepStore{}
	mover := &sweepMover{}
	pending := &sweepPending{rows: map[string][]gc.Deploy{
		"latex": {{ID: "abandoned", Mtime: pendingNow.Add(-120 * time.Hour)}},
	}}
	src := &pendingSites{sites: []sitekey.Dirname{"latex"}}

	require.NoError(t, runPendingSweep(context.Background(), src, newSweepGC(store, mover, pending), true))

	assert.Empty(t, store.tombstoned)
	assert.Zero(t, mover.moves)
}

type orderingReservations struct{ order *[]string }

func (o orderingReservations) ReclaimableReservations(context.Context, time.Time, time.Duration, int) ([]registry.Reservation, error) {
	*o.order = append(*o.order, "reservation")
	return nil, nil
}

type orderingEmitter struct{}

func (orderingEmitter) EnqueueSiteLifecycle(context.Context, []pg.SiteLifecycleEvent) error {
	return nil
}

type orderingSites struct{ order *[]string }

func (o orderingSites) SitesWithExpiredPending(context.Context, time.Time, int) ([]sitekey.Dirname, error) {
	*o.order = append(*o.order, "pending")
	return nil, nil
}

func TestTombstonePurgeHandler_SweepsPendingBeforeReleasingNames(t *testing.T) {
	var order []string
	gcw := &gcWiring{
		SiteGC:       newSweepGC(&sweepStore{}, &sweepMover{}, &sweepPending{}),
		Purge:        &gc.TombstonePurge{Store: fakeReaper{}, Now: time.Now},
		Reconciler:   &gc.Reconciler{},
		PendingSites: orderingSites{order: &order},
		Reservations: orderingReservations{order: &order},
		Lifecycle:    orderingEmitter{},
	}

	var purge worker.WorkflowDef
	for _, d := range gcWorkflowDefs(gcw, true, cleanSweep) {
		if d.Name == worker.WorkflowTombstonePurge {
			purge = d
		}
	}
	require.NoError(t, purge.Handler(context.Background(), nil))

	assert.Equal(t, []string{"pending", "reservation"}, order,
		"the reservation sweep moves the whole site prefix to trash and MovePrefix returns (0, nil) on an "+
			"empty listing, so sweeping pending after it would record a tombstone for an already-released name")
}

func TestNewGCWiring_LeavesThePendingFieldsUntypedNilWithoutARepo(t *testing.T) {
	cfg := config.Config{
		DeployPrefixFormat: "<site>.freecode.camp/deploys/<ts>-<sha>/",
		Cleanup:            config.CleanupConfig{TrashPrefix: "_trash/", BlastCap: 10},
	}
	cfg.Aliases.ProductionKeyFormat = "<site>.freecode.camp/production"
	cfg.Aliases.PreviewKeyFormat = "<site>.freecode.camp/preview"

	w, err := newGCWiring(&cfg, nil, nil, nil)
	require.NoError(t, err)

	assert.Nil(t, w.PendingSites,
		"assigning a nil *pg.Repo straight into the interface makes runPendingSweep's src == nil guard false "+
			"for a typed nil, and SitesWithExpiredPending then derefs r.pool")
	assert.Nil(t, w.SiteGC.Pending)
	assert.Nil(t, w.SiteGC.PendingIDs)
	assert.Nil(t, w.Reconciler.PendingIDs)

	for name, got := range map[string]any{
		"SiteGC.Store":      w.SiteGC.Store,
		"SiteGC.Locker":     w.SiteGC.Locker,
		"Reconciler.Store":  w.Reconciler.Store,
		"Reconciler.Locker": w.Reconciler.Locker,
		"Purge.Store":       w.Purge.Store,
		"Purge.Locker":      w.Purge.Locker,
		"Reclaim.Tombstone": w.Reclaim.Tombstone,
		"Reclaim.Locker":    w.Reclaim.Locker,
	} {
		assert.Nil(t, got, "%s holds a typed nil, so every == nil guard reading it is false comfort", name)
	}
}
