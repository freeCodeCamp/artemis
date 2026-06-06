package gc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetain_AliasMoveDoesNotBlanketHoldUnrelatedJunk(t *testing.T) {
	deploys := []Deploy{
		{ID: "d-new", Mtime: ago(4 * time.Second), HasMarker: true},
		{ID: "d-a", Mtime: ago(13 * time.Hour)},
		{ID: "d-b", Mtime: ago(14 * time.Hour)},
		{ID: "d-c", Mtime: ago(15 * time.Hour)},
		{ID: "d-d", Mtime: ago(16 * time.Hour)},
	}
	_, del := Retain(RetainInput{
		Deploys:         deploys,
		AliasTargets:    aliasSet("d-new"),
		LastAliasChange: testNow.Add(-4 * time.Second),
		Now:             testNow,
	}, testPolicy())

	assert.ElementsMatch(t, []string{"d-c", "d-d"}, delIDs(del),
		"alias move 4s ago must not shield unrelated markerless junk beyond keepN (B16)")
}

func TestRetain_ReleasedDeployHeldWithinServeCacheTTL(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(time.Hour), HasMarker: true},
		{ID: "n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "n3", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "just-released", Mtime: ago(30 * 24 * time.Hour), HasMarker: true,
			AliasReleasedAt: testNow.Add(-5 * time.Second)},
	}
	_, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	assert.NotContains(t, delIDs(del), "just-released",
		"deploy that lost alias status 5s ago outlives the serve cache (V11)")
}

func TestRetain_ReleasedDeployCollectableAfterServeCacheTTL(t *testing.T) {
	deploys := []Deploy{
		{ID: "n1", Mtime: ago(time.Hour), HasMarker: true},
		{ID: "n2", Mtime: ago(2 * time.Hour), HasMarker: true},
		{ID: "n3", Mtime: ago(3 * time.Hour), HasMarker: true},
		{ID: "released-long-ago", Mtime: ago(30 * 24 * time.Hour), HasMarker: true,
			AliasReleasedAt: testNow.Add(-30 * time.Second)},
	}
	_, del := Retain(RetainInput{Deploys: deploys, Now: testNow}, testPolicy())

	assert.Contains(t, delIDs(del), "released-long-ago",
		"past serve_cache_ttl the released deploy is collectable")
}
