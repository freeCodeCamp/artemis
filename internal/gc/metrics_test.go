package gc

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	g := newSiteGC(store, &fakeMover{})
	g.Metrics = m
	_, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.EqualValues(t, 3, testutil.ToFloat64(m.DeploysTombstoned), "3 deploys tombstoned by gc-site")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.Runs.WithLabelValues("gc-site", "ok")))

	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d", TrashedAt: ago(8 * 24 * time.Hour), Bytes: 500},
	}}
	p := newPurge(reaper, &fakeDeleter{})
	p.Metrics = m
	_, err = p.Run(context.Background(), false)
	require.NoError(t, err)

	assert.EqualValues(t, 500, testutil.ToFloat64(m.BytesReclaimed), "bytes reclaimed by tombstone-purge")
	assert.EqualValues(t, 1, testutil.ToFloat64(m.Runs.WithLabelValues("tombstone-purge", "ok")))
}

func TestMetrics_NilSafe(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	g := newSiteGC(store, &fakeMover{})
	_, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err, "nil Metrics must not panic")
}
