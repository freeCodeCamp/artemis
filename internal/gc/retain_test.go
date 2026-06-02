package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

func testPolicy() Policy {
	return Policy{
		RecentKeep:    3,
		Grace:         time.Hour,
		Retention:     7 * 24 * time.Hour,
		ServeCacheTTL: 15 * time.Second,
	}
}

func ago(d time.Duration) time.Time { return testNow.Add(-d) }

func delIDs(ds []Deploy) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}

func aliasSet(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

func TestRetain_AliasPinned(t *testing.T) {
	deploys := []Deploy{
		{ID: "d-old", Mtime: ago(30 * 24 * time.Hour), HasMarker: true},
		{ID: "d-n1", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "d-n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "d-n3", Mtime: ago(1 * time.Hour), HasMarker: true},
		{ID: "d-n4", Mtime: ago(30 * time.Minute), HasMarker: true},
	}
	_, del := Retain(RetainInput{
		Deploys:      deploys,
		AliasTargets: aliasSet("d-old"),
		Now:          testNow,
	}, testPolicy())

	assert.NotContains(t, delIDs(del), "d-old", "aliased deploy must never be deleted (V1)")
}

func TestRetain_KeepN(t *testing.T) {
	deploys := []Deploy{
		{ID: "d1", Mtime: ago(10 * 24 * time.Hour), HasMarker: true},
		{ID: "d2", Mtime: ago(20 * 24 * time.Hour), HasMarker: true},
		{ID: "d3", Mtime: ago(30 * 24 * time.Hour), HasMarker: true},
		{ID: "d4", Mtime: ago(40 * 24 * time.Hour), HasMarker: true},
		{ID: "d5", Mtime: ago(50 * 24 * time.Hour), HasMarker: true},
	}
	keep, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	assert.ElementsMatch(t, []string{"d1", "d2", "d3"}, delIDs(keep),
		"newest recentKeep=3 retained at any age (V2)")
	assert.ElementsMatch(t, []string{"d4", "d5"}, delIDs(del))
}

func TestRetain_Grace(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(time.Hour), HasMarker: true},
		{ID: "n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "n3", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "young-orphan", Mtime: ago(30 * time.Minute), HasMarker: false},
		{ID: "old-orphan", Mtime: ago(4 * time.Hour), HasMarker: false},
	}
	keep, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	assert.Contains(t, delIDs(keep), "young-orphan", "deploy younger than grace retained (V3)")
	assert.Contains(t, delIDs(del), "old-orphan", "orphan past grace, unaliased, beyond keepN is reclaimed")
}

func TestRetain_ServeCacheSafe(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(time.Hour), HasMarker: true},
		{ID: "n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "n3", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "just-superseded", Mtime: ago(30 * 24 * time.Hour), HasMarker: true},
	}

	_, delFresh := Retain(RetainInput{
		Deploys:         deploys,
		LastAliasChange: testNow.Add(-5 * time.Second),
		Now:             testNow,
	}, testPolicy())
	assert.NotContains(t, delIDs(delFresh), "just-superseded",
		"no delete within serve_cache_ttl of an alias move (V11)")

	_, delLater := Retain(RetainInput{
		Deploys:         deploys,
		LastAliasChange: testNow.Add(-30 * time.Second),
		Now:             testNow,
	}, testPolicy())
	assert.Contains(t, delIDs(delLater), "just-superseded",
		"past serve_cache_ttl the superseded deploy is collectable")
}

func TestPlan_Deterministic(t *testing.T) {
	deploys := []Deploy{
		{ID: "a", Mtime: ago(8 * 24 * time.Hour), HasMarker: true},
		{ID: "b", Mtime: ago(8 * 24 * time.Hour), HasMarker: true},
		{ID: "c", Mtime: ago(9 * 24 * time.Hour), HasMarker: true},
		{ID: "d", Mtime: ago(10 * 24 * time.Hour), HasMarker: true},
		{ID: "e", Mtime: ago(11 * 24 * time.Hour), HasMarker: true},
	}
	in := RetainInput{Deploys: deploys, Now: testNow}

	_, del1 := Retain(in, testPolicy())
	_, del2 := Retain(in, testPolicy())
	assert.Equal(t, delIDs(del1), delIDs(del2),
		"same store state + same now -> identical, stably-ordered delete set (V9)")
}

func TestRetain_CompletedRetentionWindow(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(time.Hour), HasMarker: true},
		{ID: "n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "n3", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "within-7d", Mtime: ago(3 * 24 * time.Hour), HasMarker: true},
		{ID: "beyond-7d", Mtime: ago(8 * 24 * time.Hour), HasMarker: true},
	}
	keep, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	assert.Contains(t, delIDs(keep), "within-7d", "completed deploy inside retention window retained")
	assert.Contains(t, delIDs(del), "beyond-7d", "completed deploy past retention, unaliased, beyond keepN is collectable")
}

func TestRetain_OrphanFastReclaim(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(10 * time.Minute), HasMarker: true},
		{ID: "n2", Mtime: ago(20 * time.Minute), HasMarker: true},
		{ID: "n3", Mtime: ago(30 * time.Minute), HasMarker: true},
		{ID: "orphan-2h", Mtime: ago(2 * time.Hour), HasMarker: false},
	}
	_, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	require.Contains(t, delIDs(del), "orphan-2h",
		"orphan past grace reclaimed fast, no 7d retention wait")
}
