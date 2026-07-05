package gc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeReaper struct {
	tombstones []Tombstone
	cleared    []string
}

func (f *fakeReaper) ExpiredTombstones(_ context.Context, before time.Time) ([]Tombstone, error) {
	var out []Tombstone
	for _, t := range f.tombstones {
		if t.TrashedAt.Before(before) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeReaper) ClearTombstone(_ context.Context, site, id string) error {
	f.cleared = append(f.cleared, site+"/"+id)
	for i, t := range f.tombstones {
		if t.Site == site && t.ID == id {
			f.tombstones = append(f.tombstones[:i], f.tombstones[i+1:]...)
			break
		}
	}
	return nil
}

type fakeDeleter struct {
	deleted []string
}

func (f *fakeDeleter) DeletePrefix(_ context.Context, prefix string) (int, error) {
	f.deleted = append(f.deleted, prefix)
	return 1, nil
}

func newPurge(reaper *fakeReaper, del *fakeDeleter) *TombstonePurge {
	return &TombstonePurge{
		Store:     reaper,
		Deleter:   del,
		Recovery:  7 * 24 * time.Hour,
		TrashBase: "_trash/",
		Now:       func() time.Time { return testNow },
	}
}

type fakePurgeLocker struct {
	calls int
	sites []string
}

func (f *fakePurgeLocker) WithSiteLock(_ context.Context, site string, fn func() error) error {
	f.calls++
	f.sites = append(f.sites, site)
	return fn()
}

func TestTombstonePurge(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour), Bytes: 100},
		{Site: "www", ID: "d-fresh", TrashedAt: ago(1 * 24 * time.Hour), Bytes: 50},
	}}
	del := &fakeDeleter{}
	res, err := newPurge(reaper, del).Run(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, []string{"www/d-expired"}, res.Purged, "only tombstones past the recovery window are hard-reclaimed (V5)")
	assert.Equal(t, []string{"_trash/www/d-expired/"}, del.deleted)
	assert.Equal(t, []string{"www/d-expired"}, reaper.cleared)
	assert.EqualValues(t, 100, res.BytesReclaimed)
}

func TestTombstonePurge_SitePurgeTrashLayout(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "gone", ID: "", TrashedAt: ago(10 * 24 * time.Hour), Bytes: 0},
	}}
	del := &fakeDeleter{}
	_, err := newPurge(reaper, del).Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"_trash/gone/"}, del.deleted, "empty id -> whole-site trash prefix")
}

func TestTombstonePurge_DryRun(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour)},
	}}
	del := &fakeDeleter{}
	res, err := newPurge(reaper, del).Run(context.Background(), true)
	require.NoError(t, err)

	assert.Equal(t, []string{"www/d-expired"}, res.Purged)
	assert.Empty(t, del.deleted, "dry-run reclaims nothing")
	assert.Empty(t, reaper.cleared)
}

func TestTombstonePurge_Idempotent(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour)},
	}}
	del := &fakeDeleter{}
	p := newPurge(reaper, del)

	_, err := p.Run(context.Background(), false)
	require.NoError(t, err)
	res2, err := p.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, res2.Purged, "re-run after reclaim finds no expired tombstones (V10)")
}

func TestTombstonePurge_TakesSiteLockPerTombstone(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour), Bytes: 100},
		{Site: "learn", ID: "d-old", TrashedAt: ago(9 * 24 * time.Hour), Bytes: 50},
	}}
	del := &fakeDeleter{}
	locker := &fakePurgeLocker{}
	p := newPurge(reaper, del)
	p.Locker = locker

	_, err := p.Run(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, 2, locker.calls, "one WithSiteLock call per purged tombstone")
	assert.ElementsMatch(t, []string{"www", "learn"}, locker.sites)
}

func TestTombstonePurge_DryRunDoesNotLock(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "www", ID: "d-expired", TrashedAt: ago(8 * 24 * time.Hour)},
	}}
	del := &fakeDeleter{}
	locker := &fakePurgeLocker{}
	p := newPurge(reaper, del)
	p.Locker = locker

	_, err := p.Run(context.Background(), true)
	require.NoError(t, err)

	assert.Zero(t, locker.calls, "dry-run computes the delete set but takes no lock")
}
