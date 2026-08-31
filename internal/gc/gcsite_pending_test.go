package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type fakePending struct {
	rows map[string][]Deploy
	cuts []time.Time
	err  error
}

func (p *fakePending) ExpiredPendingDeploys(_ context.Context, site sitekey.Dirname, before time.Time) ([]Deploy, error) {
	p.cuts = append(p.cuts, before)
	if p.err != nil {
		return nil, p.err
	}
	return p.rows[string(site)], nil
}

func pendingSiteGC(t *testing.T, store Store, mover Mover, pending PendingSource) *SiteGC {
	t.Helper()
	g := newSiteGC(store, mover)
	g.Pending = pending
	if fp, ok := pending.(*fakePending); ok {
		g.PendingIDs = func(_ context.Context, site sitekey.Dirname) (map[string]struct{}, error) {
			ids := map[string]struct{}{}
			for _, d := range fp.rows[string(site)] {
				ids[d.ID] = struct{}{}
			}
			return ids, nil
		}
	}
	return g
}

func TestSiteGC_ExpiresAnAbandonedDeploySession(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	res, err := pendingSiteGC(t, store, mover, pending).Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{"www/abandoned"}, store.tombstoned,
		"an init that uploaded bytes and never finalized is the orphan class reconcile had to scan R2 to "+
			"find; expiring the pending row collects it through the index instead")
	require.Len(t, mover.moves, 1)
	assert.Equal(t, [2]string{"www/deploys/abandoned/", "_trash/www/abandoned/"}, mover.moves[0])
	assert.Contains(t, res.Tombstoned, "abandoned")
}

func TestSiteGC_ExpiryCutoffSitsAtTheGraceWindow(t *testing.T) {
	pending := &fakePending{rows: map[string][]Deploy{}}

	_, err := pendingSiteGC(t, &fakeStore{deploys: map[string][]Deploy{}}, &fakeMover{}, pending).
		Run(context.Background(), "www", false)
	require.NoError(t, err)

	require.Len(t, pending.cuts, 1)
	assert.Equal(t, testNow.Add(-testPolicy().Grace), pending.cuts[0],
		"the cutoff must stay far above the 15-minute deploy JWT TTL, or a session still uploading is reaped "+
			"out from under a live client")
}

func TestSiteGC_DryRunReportsPendingWithoutTouchingAnything(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{}}
	mover := &fakeMover{}
	pending := &fakePending{rows: map[string][]Deploy{
		"www": {{ID: "abandoned", Mtime: testNow.Add(-96 * time.Hour)}},
	}}

	res, err := pendingSiteGC(t, store, mover, pending).Run(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Contains(t, res.Planned, "abandoned")
	assert.Empty(t, store.tombstoned, "a dry run must not write")
	assert.Empty(t, mover.moves, "a dry run must not move bytes")
}

func TestSiteGC_RunsWithoutAPendingSourceWired(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}}

	res, err := newSiteGC(store, &fakeMover{}).Run(context.Background(), "www", false)

	require.NoError(t, err, "pending expiry is optional wiring; its absence must not break retention")
	assert.NotEmpty(t, res.Tombstoned)
}
