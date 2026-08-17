package gc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expiredThree() []Tombstone {
	return []Tombstone{
		{Site: "www", ID: "d-mid", TrashedAt: ago(9 * 24 * time.Hour), Bytes: 10},
		{Site: "www", ID: "d-oldest", TrashedAt: ago(30 * 24 * time.Hour), Bytes: 20},
		{Site: "learn", ID: "d-newest", TrashedAt: ago(8 * 24 * time.Hour), Bytes: 30},
	}
}

func TestTombstonePurge_CapsTheRunAtTheOldestN(t *testing.T) {
	reaper := &fakeReaper{tombstones: expiredThree()}
	del := &fakeDeleter{}
	p := newPurge(reaper, del)
	p.BlastCap = 2

	res, err := p.Run(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, []string{"www/d-oldest", "www/d-mid"}, res.Purged,
		"the only irreversible job was the only destructive path without a ceiling; over-cap runs hard-"+
			"delete the most overdue trash first and leave the rest for tomorrow")
	assert.Len(t, reaper.cleared, 2)
	for _, c := range reaper.cleared {
		assert.NotContains(t, c, "d-newest", "the newest expired tombstone survives to the next run")
	}
}

func TestTombstonePurge_ZeroCapRefusesEveryHardDelete(t *testing.T) {
	reaper := &fakeReaper{tombstones: expiredThree()}
	del := &fakeDeleter{}
	p := newPurge(reaper, del)
	p.BlastCap = 0

	res, err := p.Run(context.Background(), false)
	require.NoError(t, err)

	assert.Empty(t, res.Purged)
	assert.Empty(t, del.deleted,
		"cap 0 means no ceiling was configured; the convention everywhere else is refuse, and the one "+
			"operation that cannot be undone must not be the exception")
}

func TestTombstonePurge_DryRunReportsEverythingRegardlessOfCap(t *testing.T) {
	reaper := &fakeReaper{tombstones: expiredThree()}
	p := newPurge(reaper, &fakeDeleter{})
	p.BlastCap = 1

	res, err := p.Run(context.Background(), true)
	require.NoError(t, err)

	assert.Len(t, res.Purged, 3,
		"a capped REPORT hides backlog; the cap bounds destruction, never visibility")
}

type siteFailDeleter struct {
	fakeDeleter
	failPrefix string
}

func (d *siteFailDeleter) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if strings.HasPrefix(prefix, d.failPrefix) {
		return 0, errors.New("r2 outage for this site")
	}
	return d.fakeDeleter.DeletePrefix(ctx, prefix)
}

func TestTombstonePurge_OneSiteFailureDoesNotBlockTheRest(t *testing.T) {
	reaper := &fakeReaper{tombstones: []Tombstone{
		{Site: "aaa-broken", ID: "d-1", TrashedAt: ago(30 * 24 * time.Hour), Bytes: 5},
		{Site: "zzz-healthy", ID: "d-2", TrashedAt: ago(20 * 24 * time.Hour), Bytes: 7},
	}}
	del := &siteFailDeleter{failPrefix: "_trash/aaa-broken/"}
	p := &TombstonePurge{
		Store: reaper, Deleter: del, Recovery: 7 * 24 * time.Hour,
		TrashBase: "_trash/", Now: func() time.Time { return testNow }, BlastCap: 10,
	}

	res, err := p.Run(context.Background(), false)

	require.Error(t, err, "the run still reports red so the cron check-in fails")
	assert.Contains(t, err.Error(), "aaa-broken/d-1")
	assert.Equal(t, []string{"zzz-healthy/d-2"}, res.Purged,
		"one contended or failing site used to abort the whole nightly run, silently deferring every "+
			"other site's reclamation")
}
