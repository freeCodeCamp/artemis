package gc

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type levelCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *levelCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *levelCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}
func (h *levelCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *levelCapture) WithGroup(string) slog.Handler      { return h }

func TestSelfHealing_WarnNotError(t *testing.T) {
	cap := &levelCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(cap))
	t.Cleanup(func() { slog.SetDefault(old) })

	keeper := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + keeper + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {
			{ID: keeper, Mtime: ago(2 * time.Hour)},
			{ID: "live", Mtime: ago(time.Hour)},
		}},
		aliases: map[string]struct{}{"live": {}},
	}
	_, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	var found bool
	for _, r := range cap.recs {
		if r.Message == "reconcile.aliased_bytes_missing" {
			found = true
			assert.Equal(t, slog.LevelWarn, r.Level, "self-healing success-path branch must not page")
		}
	}
	require.True(t, found, "expected the aliased_bytes_missing self-heal line")
}

type fakeReconcileStore struct {
	deploys       map[string][]Deploy
	aliases       map[string]struct{}
	aliasesAfter  map[string]struct{}
	tombstonedIDs map[string]bool
	aliasCalls    int
	reindexed     []string
	tombstoned    []string
	pruned        []string
}

func (s *fakeReconcileStore) DeploysForSite(_ context.Context, site sitekey.Dirname) ([]Deploy, error) {
	return s.deploys[string(site)], nil
}
func (s *fakeReconcileStore) AliasTargets(_ context.Context, _ sitekey.Dirname) (map[string]struct{}, time.Time, error) {
	s.aliasCalls++
	if s.aliasesAfter != nil && s.aliasCalls >= 2 {
		return s.aliasesAfter, time.Time{}, nil
	}
	return s.aliases, time.Time{}, nil
}
func (s *fakeReconcileStore) ReindexDeploy(_ context.Context, _ sitekey.Dirname, id string, _ time.Time, _ bool) (bool, error) {
	if s.tombstonedIDs[id] {
		return false, nil
	}
	s.reindexed = append(s.reindexed, id)
	return true, nil
}
func (s *fakeReconcileStore) RecordTombstone(_ context.Context, _ sitekey.Dirname, id string, _ int64) error {
	s.tombstoned = append(s.tombstoned, id)
	return nil
}
func (s *fakeReconcileStore) PruneDeploy(_ context.Context, _ sitekey.Dirname, id string) error {
	s.pruned = append(s.pruned, id)
	return nil
}

func newReconciler(lister ReconcileLister, store ReconcileStore, mover Mover) *Reconciler {
	return &Reconciler{
		Lister:       lister,
		Store:        store,
		Mover:        mover,
		Grace:        time.Hour,
		BlastCap:     defaultTestBlastCap,
		SitePrefix:   func(site sitekey.Dirname) string { return string(site) + "/deploys/" },
		DeployPrefix: func(site sitekey.Dirname, id string) string { return string(site) + "/deploys/" + id + "/" },
		TrashPrefix:  func(site sitekey.Dirname, id string) string { return "_trash/" + string(site) + "/" + id + "/" },
		LiveAliases:  func(context.Context, sitekey.Dirname) (map[string]struct{}, error) { return nil, nil },
		Now:          func() time.Time { return testNow },
		Locker:       passthroughLocker{},
	}
}

type passthroughSession struct{}

func (passthroughSession) WithSiteLock(_ context.Context, _ sitekey.Dirname, fn func() error) error {
	return fn()
}
func (passthroughSession) Close(context.Context) {}

type passthroughLocker struct{}

func (passthroughLocker) NewLockSession(context.Context) (LockSession, error) {
	return passthroughSession{}, nil
}

type fakeReconcileLister struct{ keys []string }

func (f *fakeReconcileLister) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	return keysUnder(f.keys, prefix), nil
}

func keysUnder(keys []string, prefix string) []string {
	var out []string
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func ts(d time.Duration) string {
	return testNow.Add(-d).UTC().Format("20060102-150405") + "-sha1234"
}

func TestReconcile_AuditsOrphanTombstone(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}
	rc := newReconciler(lister, store, mover)
	aud := &fakeGCAuditor{}
	rc.Audit = aud

	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, report.OrphanTombstoned)

	require.Len(t, aud.calls, 1, "each orphan tombstone records one audit row")
	assert.Equal(t, "www", aud.calls[0][0])
	assert.Equal(t, orphan, aud.calls[0][1])
}

func TestReconcile_AuditFailureDoesNotAbortSweep(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}
	rc := newReconciler(lister, store, mover)
	rc.Audit = &fakeGCAuditor{err: errAudit}

	report, err := rc.ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err, "an audit write failure must not abort the destructive reconcile sweep")
	assert.Equal(t, []string{orphan}, report.OrphanTombstoned,
		"the orphan is still tombstoned even though its audit row failed to persist")
}

func TestReconcile_Orphan(t *testing.T) {
	orphan := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + orphan + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{orphan}, report.OrphanTombstoned, "no-marker, past-grace, unindexed R2 prefix -> tombstoned (E4)")
	assert.Equal(t, []string{orphan}, store.tombstoned)
	require.Len(t, mover.moves, 1)
}

func TestReconcile_Rebuild(t *testing.T) {
	completed := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/" + completed + "/index.html",
		"www/deploys/" + completed + "/" + MarkerObjectName,
	}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Equal(t, []string{completed}, report.Reindexed, "marked-complete R2 deploy missing from PG -> re-indexed (E3)")
	assert.Empty(t, report.OrphanTombstoned, "a completed deploy is never tombstoned by reconcile")
	assert.Empty(t, mover.moves)
}

func TestReconcile_InflightSkipped(t *testing.T) {
	young := ts(5 * time.Minute)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + young + "/index.html"}}
	store := &fakeReconcileStore{deploys: map[string][]Deploy{}, aliases: map[string]struct{}{}}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)
	assert.Empty(t, report.OrphanTombstoned, "no-marker but within grace -> in-flight, left alone")
	assert.Empty(t, store.tombstoned)
}

func TestReconcile_PrunesStalePGRow(t *testing.T) {
	keeper := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + keeper + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {
			{ID: keeper, Mtime: ago(2 * time.Hour)},
			{ID: "ghost", Mtime: ago(time.Hour)},
		}},
		aliases: map[string]struct{}{},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)
	assert.Equal(t, []string{"ghost"}, report.PGPruned, "PG row with no R2 bytes pruned")
}

func TestReconcile_AliasedMissingNotPruned(t *testing.T) {
	keeper := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + keeper + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {
			{ID: keeper, Mtime: ago(2 * time.Hour)},
			{ID: "live", Mtime: ago(time.Hour)},
		}},
		aliases: map[string]struct{}{"live": {}},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)
	assert.Empty(t, report.PGPruned, "an aliased deploy whose bytes vanished is alerted, never silently pruned")
	assert.Equal(t, []string{"live"}, report.AliasedMissing)
}

func TestReconcile_AliasedOrphanNotTombstoned(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + id + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{},
		aliases: map[string]struct{}{id: {}},
	}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.NotContains(t, report.OrphanTombstoned, id,
		"an alias-pinned deploy is never tombstoned even when unindexed + marker-less + past grace (V1)")
	assert.Empty(t, mover.moves, "no R2 move of an aliased deploy")
	assert.Empty(t, store.tombstoned)
	assert.Contains(t, report.AliasedMissing, id, "surfaced as drift to alert on instead")
}

func TestReconcile_AliasRaceAfterSnapshotNotTombstoned(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + id + "/index.html"}}
	store := &fakeReconcileStore{
		deploys:      map[string][]Deploy{},
		aliases:      map[string]struct{}{},
		aliasesAfter: map[string]struct{}{id: {}},
	}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Empty(t, mover.moves, "deploy aliased after the snapshot read must not be tombstoned (V1 TOCTOU)")
	assert.Empty(t, store.tombstoned)
	assert.NotContains(t, report.OrphanTombstoned, id)
}

func TestReconcile_ConsistentNoDrift(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{"www/deploys/" + id + "/index.html"}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{"www": {{ID: id, Mtime: ago(2 * time.Hour)}}},
		aliases: map[string]struct{}{},
	}
	report, err := newReconciler(lister, store, &fakeMover{}).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)
	assert.Empty(t, report.Reindexed)
	assert.Empty(t, report.OrphanTombstoned)
	assert.Empty(t, report.PGPruned)
}

func TestReconcile_AliasedWithMarker_ReindexedNotPaged(t *testing.T) {
	id := ts(2 * time.Hour)
	lister := &fakeReconcileLister{keys: []string{
		"www/deploys/" + id + "/index.html",
		"www/deploys/" + id + "/" + MarkerObjectName,
	}}
	store := &fakeReconcileStore{
		deploys: map[string][]Deploy{},
		aliases: map[string]struct{}{id: {}},
	}
	mover := &fakeMover{}

	report, err := newReconciler(lister, store, mover).ReconcileSite(context.Background(), "www", false)
	require.NoError(t, err)

	assert.Contains(t, report.Reindexed, id, "aliased + marker + unindexed self-heals via reindex")
	assert.NotContains(t, report.AliasedMissing, id, "a self-healed deploy must not page as dangerous drift")
	assert.Equal(t, []string{id}, store.reindexed, "reindex persisted to the store")
	assert.Empty(t, mover.moves, "self-healed deploy is not tombstoned")
}
