package backfill

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/r2"
)

type fakeLister struct {
	sites      []string
	byPfx      map[string][]string
	bytesByPfx map[string]int64
	aliases    map[string]string
}

func (f *fakeLister) ListSites(context.Context) ([]string, error) { return f.sites, nil }

func (f *fakeLister) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	return f.byPfx[prefix], nil
}

func (f *fakeLister) PrefixBytes(_ context.Context, prefix string) (int64, error) {
	return f.bytesByPfx[prefix], nil
}

func (f *fakeLister) GetAlias(_ context.Context, key string) (string, error) {
	v, ok := f.aliases[key]
	if !ok {
		return "", r2.ErrNotFound
	}
	return v, nil
}

type idxDeploy struct {
	site, id  string
	mtime     time.Time
	bytes     int64
	hasMarker bool
}

type idxAlias struct {
	site, name, deployID string
}

type fakeIndexer struct {
	deploys []idxDeploy
	aliases []idxAlias
}

func (f *fakeIndexer) UpsertDeploy(_ context.Context, site, id string, mtime time.Time, bytes int64, hasMarker bool, _ string) error {
	f.deploys = append(f.deploys, idxDeploy{site, id, mtime, bytes, hasMarker})
	return nil
}

func (f *fakeIndexer) UpsertAlias(_ context.Context, site, name, deployID string, _ time.Time) error {
	f.aliases = append(f.aliases, idxAlias{site, name, deployID})
	return nil
}

func TestBackfill(t *testing.T) {
	lister := &fakeLister{
		sites: []string{"www", "learn"},
		byPfx: map[string][]string{
			"www/deploys/": {
				"www/deploys/20260420-141522-abc1234/index.html",
				"www/deploys/20260420-141522-abc1234/_artemis_meta.json",
				"www/deploys/20260101-090000-old0001/index.html",
			},
			"learn/deploys/": {
				"learn/deploys/20260515-120000-def5678/index.html",
			},
		},
		bytesByPfx: map[string]int64{
			"www/deploys/20260420-141522-abc1234/":   300,
			"www/deploys/20260101-090000-old0001/":   100,
			"learn/deploys/20260515-120000-def5678/": 50,
		},
		aliases: map[string]string{
			"www/production": "20260420-141522-abc1234",
			"www/preview":    "20260101-090000-old0001",
			"learn/preview":  "20260515-120000-def5678",
		},
	}
	idx := &fakeIndexer{}
	b := &Backfill{Lister: lister, Indexer: idx, Now: func() time.Time {
		return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	}}

	res, err := b.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, res.Sites)
	assert.Equal(t, 3, res.Deploys, "two www deploys + one learn deploy (marker is not its own deploy)")
	assert.Equal(t, 3, res.Aliases, "www prod+preview, learn preview (learn prod absent)")

	byID := map[string]idxDeploy{}
	for _, d := range idx.deploys {
		byID[d.id] = d
	}
	require.Contains(t, byID, "20260420-141522-abc1234")
	assert.True(t, byID["20260420-141522-abc1234"].hasMarker, "deploy with _artemis_meta.json marked completed")
	assert.False(t, byID["20260101-090000-old0001"].hasMarker, "deploy without marker is an orphan")
	assert.Equal(t, time.Date(2026, 4, 20, 14, 15, 22, 0, time.UTC), byID["20260420-141522-abc1234"].mtime,
		"mtime parsed from deploy-id timestamp")
	assert.Equal(t, int64(300), byID["20260420-141522-abc1234"].bytes, "backfill records per-deploy R2 byte size")
	assert.Equal(t, int64(100), byID["20260101-090000-old0001"].bytes)
}

func TestBackfill_AliasKeyIsR2DirRelative(t *testing.T) {
	dir := "www.freecode.camp"
	lister := &fakeLister{
		sites: []string{dir},
		byPfx: map[string][]string{dir + "/deploys/": {dir + "/deploys/20260420-141522-abc1234/index.html"}},
		aliases: map[string]string{
			dir + "/production": "20260420-141522-abc1234",
			dir + "/preview":    "20260420-141522-abc1234",
		},
	}
	idx := &fakeIndexer{}
	b := &Backfill{Lister: lister, Indexer: idx, Now: func() time.Time { return time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC) }}

	res, err := b.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Deploys)
	assert.Equal(t, 2, res.Aliases,
		"alias key is the R2-dir-relative literal <dir>/<mode>; the dir from ListSites already carries the .freecode.camp suffix, so the slug-templated ALIAS_*_KEY_FORMAT must NOT be re-applied")
}
