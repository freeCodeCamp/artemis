package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/r2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubAuditRecorder struct {
	err  error
	n    int
	last pg.AuditEvent
}

func (s *stubAuditRecorder) RecordAudit(_ context.Context, e pg.AuditEvent) error {
	s.n++
	s.last = e
	return s.err
}

func trapAuditCapture(t *testing.T) *struct {
	op  string
	err error
} {
	t.Helper()
	got := &struct {
		op  string
		err error
	}{}
	orig := captureAuditFailure
	captureAuditFailure = func(op string, err error) { got.op = op; got.err = err }
	t.Cleanup(func() { captureAuditFailure = orig })
	return got
}

func TestGCTombstoneAuditor_CapturesAuditWriteFailure(t *testing.T) {
	got := trapAuditCapture(t)
	writeErr := errors.New("pg write failed")
	a := gcTombstoneAuditor{repo: &stubAuditRecorder{err: writeErr}, actor: "system:gc", action: "gc.tombstone"}

	err := a.AuditTombstone(context.Background(), "www", "id1")
	require.ErrorIs(t, err, writeErr, "audit failure must still propagate so the sweep logs it fail-soft")
	assert.Equal(t, "audit.record", got.op,
		"a background tombstone audit-write failure must raise the documented op=audit.record Sentry issue")
	assert.ErrorIs(t, got.err, writeErr)
}

func TestGCPurgeAuditor_CapturesAuditWriteFailure(t *testing.T) {
	got := trapAuditCapture(t)
	writeErr := errors.New("pg write failed")
	a := gcPurgeAuditor{repo: &stubAuditRecorder{err: writeErr}}

	err := a.RecordPurge(context.Background(), "www", "id1")
	require.ErrorIs(t, err, writeErr)
	assert.Equal(t, "audit.record", got.op,
		"a background purge audit-write failure must also raise op=audit.record")
}

func TestGCTombstoneAuditor_NoCaptureOnSuccess(t *testing.T) {
	got := trapAuditCapture(t)
	a := gcTombstoneAuditor{repo: &stubAuditRecorder{}, actor: "system:gc", action: "gc.tombstone"}

	require.NoError(t, a.AuditTombstone(context.Background(), "www", "id1"))
	assert.Empty(t, got.op, "a successful audit write must not raise a Sentry issue")
}

type recordingAliasGetter struct {
	keys   []string
	values map[string]string
}

func (g *recordingAliasGetter) GetAlias(_ context.Context, key string) (string, error) {
	g.keys = append(g.keys, key)
	if v, ok := g.values[key]; ok {
		return v, nil
	}
	return "", r2.ErrNotFound
}

func TestNewLiveAliasReader_KeyMatchesWritePath(t *testing.T) {
	const prodFmt = "<site>.freecode.camp/production"
	getter := &recordingAliasGetter{values: map[string]string{
		"www.freecode.camp/production": "20260101-000000-abc1234",
	}}
	read, err := newLiveAliasReader(getter, prodFmt)
	require.NoError(t, err)

	live, err := read(context.Background(), "www")
	require.NoError(t, err)

	assert.Equal(t, []string{"www.freecode.camp/production"}, getter.keys,
		"GC live re-read must query the write-path key (ReplaceAll <site>), not a slash-derived tail")
	_, ok := live["20260101-000000-abc1234"]
	assert.True(t, ok, "the live deploy behind the prod alias must be detected by the pre-delete safety net")
}

func TestNewLiveAliasReader_RequiresSiteToken(t *testing.T) {
	_, err := newLiveAliasReader(&recordingAliasGetter{}, "production/only")
	require.Error(t, err, "an alias format missing <site> must fail boot, not silently mis-derive keys")
}

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

func TestNewGCWiring_PlumbsBlastCapAndPrefixes(t *testing.T) {
	cfg := &config.Config{
		DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/",
		Aliases: config.AliasConfig{
			ProductionKeyFormat: "<site>/production",
			PreviewKeyFormat:    "<site>/preview",
		},
		Cleanup: config.CleanupConfig{
			BlastCap:      5,
			RetentionDays: 7,
			RecoveryDays:  3,
			TrashPrefix:   "_trash/",
		},
	}
	repo := &pg.Repo{}
	r2c := &r2.Client{}

	w, err := newGCWiring(cfg, repo, r2c)
	require.NoError(t, err)
	require.NotNil(t, w)

	assert.Same(t, repo, w.Repo, "repo must be plumbed through")
	assert.Equal(t, 5, w.SiteGC.BlastCap, "BlastCap=0 would disable the mass-delete safety cap")
	assert.Equal(t, 7*24*time.Hour, w.SiteGC.Policy.Retention, "policy retention must derive from RetentionDays")
	assert.Equal(t, "_trash/", w.Purge.TrashBase, "purge must scan the configured trash base")
	assert.Equal(t, 3*24*time.Hour, w.Purge.Recovery, "recovery window must derive from RecoveryDays")

	require.NotNil(t, w.SiteGC.DeployPrefix)
	require.NotNil(t, w.SiteGC.TrashPrefix)
	require.NotNil(t, w.Reconciler.SitePrefix)
	require.NotNil(t, w.Reconciler.DeployPrefix)

	assert.Equal(t, "www/deploys/id/", w.SiteGC.DeployPrefix("www", "id"),
		"a wrong deploy-prefix closure would mass-move the wrong R2 prefix")
	assert.Equal(t, "_trash/www/id/", w.SiteGC.TrashPrefix("www", "id"))
	assert.Equal(t, "www/deploys/", w.Reconciler.SitePrefix("www"))
}

func TestNewGCWiring_RejectsBadFormat(t *testing.T) {
	cfg := &config.Config{
		DeployPrefixFormat: "<site>/deploys/",
		Cleanup:            config.CleanupConfig{BlastCap: 5, TrashPrefix: "_trash/"},
	}
	w, err := newGCWiring(cfg, &pg.Repo{}, &r2.Client{})
	require.Error(t, err, "a format missing the deploy-id token must fail boot wiring, not produce a degenerate prefix fn")
	require.Nil(t, w)
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
