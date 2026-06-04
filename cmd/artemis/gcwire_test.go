package main

import (
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRepoQueue_RequiresDatabase(t *testing.T) {
	q, err := openRepoQueue(nil)
	require.Error(t, err, "repo feature without a database must be rejected at boot")
	require.Nil(t, q)
}

func TestOpenRepoQueue_IsPostgresBacked(t *testing.T) {
	q, err := openRepoQueue(&pg.DB{})
	require.NoError(t, err)
	_, ok := q.(*pg.RepoQueue)
	assert.True(t, ok, "repo queue must be backed by pg.RepoQueue")
}

func TestBootWiringProdLayout(t *testing.T) {
	cases := []struct {
		name       string
		format     string
		trashBase  string
		site       string
		id         string
		wantSite   string
		wantDeploy string
		wantTrash  string
	}{
		{
			name:       "default-dev-layout",
			format:     "<site>/deploys/<ts>-<sha>/",
			trashBase:  "_trash/",
			site:       "www",
			id:         "20260101-000000-abc1234",
			wantSite:   "www/deploys/",
			wantDeploy: "www/deploys/20260101-000000-abc1234/",
			wantTrash:  "_trash/www/20260101-000000-abc1234/",
		},
		{
			name:       "prod-dirname-layout",
			format:     "<site>.freecode.camp/deploys/<ts>-<sha>/",
			trashBase:  "_trash/",
			site:       "www.freecode.camp",
			id:         "20260101-000000-abc1234",
			wantSite:   "www.freecode.camp/deploys/",
			wantDeploy: "www.freecode.camp/deploys/20260101-000000-abc1234/",
			wantTrash:  "_trash/www.freecode.camp/20260101-000000-abc1234/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := newGCLayout(tc.format, tc.trashBase)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSite, l.sitePrefix(tc.site), "sitePrefix")
			assert.Equal(t, tc.wantDeploy, l.deployPrefix(tc.site, tc.id), "deployPrefix")
			assert.Equal(t, tc.wantTrash, l.trashPrefix(tc.site, tc.id), "trashPrefix")
		})
	}
}

func TestBootWiring_LayoutRejectsBadFormat(t *testing.T) {
	_, err := newGCLayout("<site>/deploys/", "_trash/")
	require.Error(t, err, "format without the deploy-id token must be rejected")
}

func TestGCPolicyFromConfig(t *testing.T) {
	p := gcPolicy(config.CleanupConfig{
		RecentKeep:    3,
		Grace:         time.Hour,
		RetentionDays: 7,
		ServeCacheTTL: 15 * time.Second,
	})
	assert.Equal(t, 3, p.RecentKeep)
	assert.Equal(t, time.Hour, p.Grace)
	assert.Equal(t, 7*24*time.Hour, p.Retention)
	assert.Equal(t, 15*time.Second, p.ServeCacheTTL)
}
