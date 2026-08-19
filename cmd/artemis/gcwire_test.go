package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/config"
	"github.com/freeCodeCamp/artemis/internal/handler"
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
	read, err := newLiveAliasReader(getter, domainFormat, prodFmt)
	require.NoError(t, err)

	live, err := read(context.Background(), "www.freecode.camp")
	require.NoError(t, err)

	assert.Equal(t, []string{"www.freecode.camp/production"}, getter.keys,
		"both call sites pass the storage dirname the sweep enumerates, so substituting it into "+
			"<site> renders www.freecode.camp.freecode.camp/production and 404s forever")
	_, ok := live["20260101-000000-abc1234"]
	assert.True(t, ok, "the live deploy behind the prod alias must be detected by the pre-delete safety net")
}

func TestNewLiveAliasReader_ReadsTheSameKeyspaceTheSweepEnumerates(t *testing.T) {
	tmpl, err := handler.NewDeployPrefixTemplate(domainFormat)
	require.NoError(t, err)
	layout, err := newGCLayout(domainFormat, "_trash/")
	require.NoError(t, err)

	getter := &recordingAliasGetter{}
	read, err := newLiveAliasReader(getter, domainFormat,
		"<site>.freecode.camp/production", "<site>.freecode.camp/preview")
	require.NoError(t, err)

	dirname := tmpl.SiteDirname("test")
	_, err = read(context.Background(), dirname)
	require.NoError(t, err)

	require.Len(t, getter.keys, 2)
	for _, k := range getter.keys {
		assert.True(t, strings.HasPrefix(k, layout.sitePrefix(dirname)[:len(dirname)+1]),
			"alias key %q must sit under the same site directory the sweep lists, or the "+
				"pre-delete safety net reads a prefix no alias was ever written to", k)
	}
}

func TestNewLiveAliasReader_RequiresSiteToken(t *testing.T) {
	_, err := newLiveAliasReader(&recordingAliasGetter{}, domainFormat, "production/only")
	require.Error(t, err, "an alias format missing <site> must fail boot, not silently mis-derive keys")
}

func TestNewLiveAliasReader_RejectsASiteSegmentTheDeployPrefixDoesNotShare(t *testing.T) {
	_, err := newLiveAliasReader(&recordingAliasGetter{}, domainFormat, "<site>.preview.freecode.camp/production")
	require.Error(t, err,
		"an alias format whose site segment differs from the deploy prefix's cannot be reached from a "+
			"dirname; boot must refuse rather than 404 silently for every site")
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
	assert.Equal(t, 5, w.Purge.BlastCap,
		"the irreversible job shares the configured ceiling; an unwired cap defaults to 0 which refuses every hard delete")
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

func TestNewGCWiring_AuditActorActionSplit(t *testing.T) {
	cfg := &config.Config{
		DeployPrefixFormat: "<site>/deploys/<ts>-<sha>/",
		Aliases: config.AliasConfig{
			ProductionKeyFormat: "<site>/production",
			PreviewKeyFormat:    "<site>/preview",
		},
		Cleanup: config.CleanupConfig{BlastCap: 5, RetentionDays: 7, RecoveryDays: 3, TrashPrefix: "_trash/"},
	}

	w, err := newGCWiring(cfg, &pg.Repo{}, &r2.Client{})
	require.NoError(t, err)

	ta, ok := w.SiteGC.Audit.(gcTombstoneAuditor)
	require.True(t, ok, "SiteGC.Audit must be the gcTombstoneAuditor adapter")
	assert.Equal(t, "system:gc", ta.actor, "gc-sweep tombstones must be attributed to system:gc in the durable audit_log")
	assert.Equal(t, "gc.tombstone", ta.action)

	ra, ok := w.Reconciler.Audit.(gcTombstoneAuditor)
	require.True(t, ok, "Reconciler.Audit must be the gcTombstoneAuditor adapter")
	assert.Equal(t, "system:reconcile", ra.actor,
		"reconcile-sourced tombstones must be attributed to system:reconcile, not mislabelled as a gc sweep")
	assert.Equal(t, "gc.reconcile", ra.action)
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

func TestNewLiveAliasReader_RejectsASiteTokenOutsideTheSiteSegment(t *testing.T) {
	_, err := newLiveAliasReader(&recordingAliasGetter{}, domainFormat,
		"<site>.freecode.camp/aliases-<site>/production")
	require.Error(t, err,
		"the reader substitutes nothing after the site segment, so a surviving <site> is fetched literally "+
			"and 404s for every site — the same silent-inert failure this constructor exists to refuse")
}

func prodSlugFn(t *testing.T) func(string) (string, bool) {
	t.Helper()
	tmpl, err := handler.NewDeployPrefixTemplate("<site>.freecode.camp/deploys/<ts>-<sha>/")
	require.NoError(t, err)
	return tmpl.SiteSlug
}

func TestGCTombstoneAuditor_RecordsTheRegistrySlugNotTheStorageDirname(t *testing.T) {
	repo := &stubAuditRecorder{}
	a := gcTombstoneAuditor{repo: repo, actor: "system:gc", action: "gc.tombstone", toSlug: prodSlugFn(t)}

	require.NoError(t, a.AuditTombstone(context.Background(), "test.freecode.camp", "id1"))

	assert.Equal(t, "test", repo.last.Site,
		"audit_log.site is read back by DeployActors and by ?site= on the audit API, both of which are "+
			"handed the URL slug; a dirname here is invisible to every reader")
}

func TestGCPurgeAuditor_RecordsTheRegistrySlugNotTheStorageDirname(t *testing.T) {
	repo := &stubAuditRecorder{}
	a := gcPurgeAuditor{repo: repo, toSlug: prodSlugFn(t)}

	require.NoError(t, a.RecordPurge(context.Background(), "test.freecode.camp", "id1"))

	assert.Equal(t, "test", repo.last.Site)
}

func TestGCTombstoneAuditor_KeepsTheRawSiteWhenTheDirnameDoesNotMap(t *testing.T) {
	got := trapAuditCapture(t)
	repo := &stubAuditRecorder{}
	a := gcTombstoneAuditor{repo: repo, actor: "system:gc", action: "gc.tombstone", toSlug: prodSlugFn(t)}

	require.NoError(t, a.AuditTombstone(context.Background(), "www.example.com", "id1"))

	assert.Equal(t, "www.example.com", repo.last.Site,
		"an unmappable dirname must still produce a row — losing the audit record is worse than an "+
			"off-keyspace one, and audit_log is append-only so nothing can repair a gap later")
	assert.Equal(t, true, repo.last.Detail["site_unmapped"],
		"the row must say so itself, so a reader can tell this site value apart from a slug")
	assert.Equal(t, "audit.site_unmapped", got.op,
		"and it must page, because it means the sweep is walking prefixes the deploy format cannot render")
}

func TestGCPurgeAuditor_KeepsTheRawSiteWhenTheDirnameDoesNotMap(t *testing.T) {
	got := trapAuditCapture(t)
	repo := &stubAuditRecorder{}
	a := gcPurgeAuditor{repo: repo, toSlug: prodSlugFn(t)}

	require.NoError(t, a.RecordPurge(context.Background(), "www.example.com", "id1"))

	assert.Equal(t, "www.example.com", repo.last.Site)
	assert.Equal(t, "audit.site_unmapped", got.op)
}

func TestGCTombstoneAuditor_PassesTheSiteThroughWhenNoConverterIsWired(t *testing.T) {
	repo := &stubAuditRecorder{}
	a := gcTombstoneAuditor{repo: repo, actor: "system:gc", action: "gc.tombstone"}

	require.NoError(t, a.AuditTombstone(context.Background(), "www", "id1"))

	assert.Equal(t, "www", repo.last.Site)
	assert.Nil(t, repo.last.Detail, "a nil converter is not an anomaly, so it must not flag the row")
}

func TestNewGCWiring_GivesEveryAuditorTheSlugConverter(t *testing.T) {
	cfg := &config.Config{
		DeployPrefixFormat: "<site>.freecode.camp/deploys/<ts>-<sha>/",
		Cleanup:            config.CleanupConfig{TrashPrefix: "_trash/", BlastCap: 10},
	}
	cfg.Aliases.ProductionKeyFormat = "<site>.freecode.camp/production"
	cfg.Aliases.PreviewKeyFormat = "<site>.freecode.camp/preview"

	w, err := newGCWiring(cfg, nil, nil)
	require.NoError(t, err)

	for name, got := range map[string]siteSlugFn{
		"SiteGC.Audit":          w.SiteGC.Audit.(gcTombstoneAuditor).toSlug,
		"Reconciler.Audit":      w.Reconciler.Audit.(gcTombstoneAuditor).toSlug,
		"Reconciler.PruneAudit": w.Reconciler.PruneAudit.(gcTombstoneAuditor).toSlug,
		"Purge.Audit":           w.Purge.Audit.(gcPurgeAuditor).toSlug,
	} {
		require.NotNil(t, got, "%s must convert to the registry keyspace; a nil converter silently "+
			"reverts that writer to dirnames and no test downstream would notice", name)
		slug, ok := got("test.freecode.camp")
		require.True(t, ok)
		assert.Equal(t, "test", slug, "%s", name)
	}
}
