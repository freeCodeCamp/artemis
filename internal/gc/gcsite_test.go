package gc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	deploys      map[string][]Deploy
	targetsSeq   []map[string]struct{}
	lastChange   time.Time
	aliasCalls   int
	tombstoned   []string
	tombstoneErr error
}

func (s *fakeStore) DeploysForSite(_ context.Context, site string) ([]Deploy, error) {
	return s.deploys[site], nil
}

func (s *fakeStore) AliasTargets(_ context.Context, _ string) (map[string]struct{}, time.Time, error) {
	idx := s.aliasCalls
	s.aliasCalls++
	if idx >= len(s.targetsSeq) {
		idx = len(s.targetsSeq) - 1
	}
	if idx < 0 {
		return map[string]struct{}{}, s.lastChange, nil
	}
	return s.targetsSeq[idx], s.lastChange, nil
}

func (s *fakeStore) Tombstone(_ context.Context, site string, d Deploy) error {
	if s.tombstoneErr != nil {
		return s.tombstoneErr
	}
	s.tombstoned = append(s.tombstoned, site+"/"+d.ID)
	return nil
}

type fakeMover struct {
	moves [][2]string
}

func (m *fakeMover) MovePrefix(_ context.Context, src, dst string) (int, error) {
	m.moves = append(m.moves, [2]string{src, dst})
	return 1, nil
}

func newSiteGC(store Store, mover Mover) *SiteGC {
	return &SiteGC{
		Store:        store,
		Mover:        mover,
		Policy:       testPolicy(),
		BlastCap:     0,
		DeployPrefix: func(site, id string) string { return site + "/deploys/" + id + "/" },
		TrashPrefix:  func(site, id string) string { return "_trash/" + site + "/" + id + "/" },
		LiveAliases: func(_ context.Context, _ string) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		},
		Now: func() time.Time { return testNow },
	}
}

func sixOld() []Deploy {
	return oldDeploys(6, 100)
}

func TestGC_AliasPinned(t *testing.T) {
	ds := sixOld()
	aliased := ds[len(ds)-1].ID
	store := &fakeStore{
		deploys:    map[string][]Deploy{"www": ds},
		targetsSeq: []map[string]struct{}{aliasSet(aliased)},
	}
	mover := &fakeMover{}
	res, err := newSiteGC(store, mover).Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.NotContains(t, res.Tombstoned, aliased, "aliased deploy never tombstoned (V1)")
	for _, m := range mover.moves {
		assert.NotContains(t, m[0], aliased, "no move of an aliased deploy")
	}
}

func TestGC_PromoteMidRun(t *testing.T) {
	ds := sixOld()
	victim := ds[len(ds)-1].ID
	store := &fakeStore{
		deploys:    map[string][]Deploy{"www": ds},
		targetsSeq: []map[string]struct{}{{}}, // plan-time: nothing aliased
	}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)
	g.LiveAliases = func(_ context.Context, _ string) (map[string]struct{}, error) {
		return aliasSet(victim), nil // R2 live read: alias moved onto victim mid-run
	}
	res, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Contains(t, res.Planned, victim, "victim was in the plan")
	assert.Contains(t, res.SkippedAliased, victim, "TOCTOU re-check skips a deploy aliased mid-run (V1)")
	assert.NotContains(t, res.Tombstoned, victim)
	for _, m := range mover.moves {
		assert.NotContains(t, m[0], victim)
	}
}

func TestGC_InflightProtected(t *testing.T) {
	ds := []Deploy{
		{ID: "n1", Mtime: ago(10 * time.Minute), Bytes: 1, HasMarker: true},
		{ID: "n2", Mtime: ago(20 * time.Minute), Bytes: 1, HasMarker: true},
		{ID: "n3", Mtime: ago(30 * time.Minute), Bytes: 1, HasMarker: true},
		{ID: "uploading", Mtime: ago(2 * time.Minute), Bytes: 1, HasMarker: false},
	}
	store := &fakeStore{deploys: map[string][]Deploy{"www": ds}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	res, err := newSiteGC(store, mover).Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.NotContains(t, res.Tombstoned, "uploading", "in-flight (young, no marker) deploy protected (V4)")
	assert.Empty(t, mover.moves)
}

func TestGC_Idempotent(t *testing.T) {
	ds := sixOld()
	store := &fakeStore{deploys: map[string][]Deploy{"www": ds}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)

	res1, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)
	require.Len(t, res1.Tombstoned, 3)

	store.deploys["www"] = nil
	res2, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)
	assert.Empty(t, res2.Tombstoned, "re-run after reclaim tombstones nothing new (V10)")
}

func TestGC_PerSiteScoped(t *testing.T) {
	store := &fakeStore{
		deploys:    map[string][]Deploy{"www": sixOld(), "learn": sixOld()},
		targetsSeq: []map[string]struct{}{{}},
	}
	mover := &fakeMover{}
	_, err := newSiteGC(store, mover).Run(context.Background(), "www", false)
	require.NoError(t, err)

	for _, m := range mover.moves {
		assert.True(t, strings.HasPrefix(m[0], "www/"), "GC of www must only touch www prefixes (V7 site scope)")
	}
	for _, ts := range store.tombstoned {
		assert.True(t, strings.HasPrefix(ts, "www/"), "tombstones scoped to the target site")
	}
}

func TestGC_DryRun(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	res, err := newSiteGC(store, mover).Run(context.Background(), "www", true)
	require.NoError(t, err)

	assert.Len(t, res.Planned, 3, "dry-run still computes the plan")
	assert.Empty(t, res.Tombstoned, "dry-run mutates nothing")
	assert.Empty(t, mover.moves)
	assert.Empty(t, store.tombstoned)
}

func TestGC_BlastCapAborts(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": oldDeploys(10, 1)}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)
	g.BlastCap = 5
	res, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)

	assert.True(t, res.Aborted)
	assert.Empty(t, res.Tombstoned, "aborted plan mutates nothing (V6)")
	assert.Empty(t, mover.moves)
}
