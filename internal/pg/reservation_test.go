package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

const (
	reservationSlug    = sitekey.Slug("palette")
	reservationDirname = sitekey.Dirname("palette.freecode.camp")
)

func newReservationFixture(t *testing.T) (*RegistryStore, *Repo, context.Context) {
	t.Helper()
	repo := newTestRepo(t)
	store := NewRegistryStore(&DB{Pool: repo.pool})
	ctx := context.Background()
	_, err := store.Register(ctx, reservationSlug, []string{"staff"}, "alice")
	require.NoError(t, err)
	require.NoError(t, repo.UpsertAlias(ctx, reservationDirname, "production", "20260101-000000-aaaaaaa", time.Now().UTC()))
	require.NoError(t, repo.UpsertAlias(ctx, reservationDirname, "preview", "20260102-000000-bbbbbbb", time.Now().UTC()))
	return store, repo, ctx
}

func TestRegistryStore_ReserveFlipsStateCapturesPrevAliasesAndClearsTheIndex(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	until := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	res, err := store.Reserve(ctx, reservationSlug, reservationDirname, until, "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	assert.Equal(t, "20260101-000000-aaaaaaa", res.PrevProduction)
	assert.Equal(t, "20260102-000000-bbbbbbb", res.PrevPreview)
	assert.WithinDuration(t, until, res.ReservedUntil, time.Second)
	assert.Equal(t, "bob", res.ReservedBy)

	site, err := store.GetSite(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, site.IsReserved(), "the name is held; serving has already stopped")
	assert.WithinDuration(t, until, site.ReservedUntil, time.Second)

	targets, _, err := repo.AliasTargets(ctx, reservationDirname)
	require.NoError(t, err)
	assert.Empty(t, targets, "the alias index must agree with R2, where the alias objects are already gone")
}

func TestRegistryStore_ReserveOnAReservedRowKeepsTheFirstDeadline(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	first := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)

	firstRes, err := store.Reserve(ctx, reservationSlug, reservationDirname, first, "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	second, err := store.Reserve(ctx, reservationSlug, reservationDirname, first.Add(48*time.Hour), "carol", registry.ObservedAliases{})
	require.NoError(t, err)

	assert.WithinDuration(t, firstRes.ReservedUntil, second.ReservedUntil, time.Second,
		"a repeated takedown must not push the reclaim further out every time it is retried")
	assert.Equal(t, "bob", second.ReservedBy)
	assert.Equal(t, "20260101-000000-aaaaaaa", second.PrevProduction,
		"the second call must not overwrite the captured aliases with the emptied index")
}

func TestRegistryStore_ReserveOnAnAbsentSlugIsNotFound(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, "absent", "absent.freecode.camp", time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	assert.ErrorIs(t, err, registry.ErrNotFound)
}

func TestRegistryStore_RegisterOnAReservedSlugReturnsErrReserved(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	_, err = store.Register(ctx, reservationSlug, []string{"staff"}, "mallory")
	assert.ErrorIs(t, err, registry.ErrReserved,
		"re-registering a reserved name would hand the new owner the previous owner's retained bytes")
	assert.NotErrorIs(t, err, registry.ErrAlreadyExists)
}

func TestRegistryStore_RegisterOnALiveSlugStillReturnsErrAlreadyExists(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Register(ctx, reservationSlug, []string{"staff"}, "mallory")
	assert.ErrorIs(t, err, registry.ErrAlreadyExists)
	assert.NotErrorIs(t, err, registry.ErrReserved)
}

func TestRegistryStore_UndeleteRestoresTheRowAndHandsBackThePreviousAliases(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	res, err := store.Undelete(ctx, reservationSlug)
	require.NoError(t, err)
	assert.Equal(t, "20260101-000000-aaaaaaa", res.PrevProduction)
	assert.Equal(t, "20260102-000000-bbbbbbb", res.PrevPreview)

	site, err := store.GetSite(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, site.IsReserved())
	assert.True(t, site.ReservedUntil.IsZero(), "an active row carries no deadline")
}

func TestRegistryStore_UndeleteOnAnActiveRowIsNotReserved(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Undelete(ctx, reservationSlug)
	assert.ErrorIs(t, err, registry.ErrNotFound,
		"undelete on a live site would overwrite its production alias with a stale pointer")
}

func TestRegistryStore_ExpiredReservationsSelectsOnlyPastDeadlines(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Register(ctx, "livehold", []string{"staff"}, "alice")
	require.NoError(t, err)

	now := time.Now().UTC()
	_, err = store.Reserve(ctx, reservationSlug, reservationDirname, now.Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
	_, err = store.Reserve(ctx, "livehold", "livehold.freecode.camp", now.Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	expired, err := store.ExpiredReservations(ctx, now, 10)
	require.NoError(t, err)

	require.Len(t, expired, 1, "a reservation still inside its grace window is not reclaimable")
	assert.Equal(t, reservationSlug, expired[0].Slug)
}

func TestRegistryStore_ExpiredReservationsHonoursItsLimit(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	now := time.Now().UTC()
	for _, slug := range []sitekey.Slug{"alpha", "bravo"} {
		_, err := store.Register(ctx, slug, []string{"staff"}, "alice")
		require.NoError(t, err)
		_, err = store.Reserve(ctx, slug, sitekey.Dirname(string(slug)+".freecode.camp"), now.Add(-time.Hour), "bob", registry.ObservedAliases{})
		require.NoError(t, err)
	}
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, now.Add(-time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	expired, err := store.ExpiredReservations(ctx, now, 2)
	require.NoError(t, err)
	assert.Len(t, expired, 2)
}

func TestRegistryStore_ReserveDoesNotEnqueueSiteChanged(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	var n int
	require.NoError(t, repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE topic = $1`, TopicSiteChanged).Scan(&n))
	assert.Zero(t, n,
		"gc-site must never run on a reserved site: it would tombstone the un-aliased previous deploy "+
			"and leave undelete with nothing to restore")
}

func TestRegistryStore_SitesListsAReservedNameWithItsDeadline(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, until, "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	sites, err := store.Sites(ctx)
	require.NoError(t, err)

	require.Len(t, sites, 1, "staff must be able to find what there is to undelete")
	assert.True(t, sites[0].IsReserved())
	assert.WithinDuration(t, until, sites[0].ReservedUntil, time.Second)
}

func TestMigrate_SitesReservedRowMustCarryADeadline(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	_ = store

	_, err := repo.pool.Exec(ctx,
		`UPDATE sites SET state = 'reserved', reserved_until = NULL WHERE slug = $1`, reservationSlug)

	require.Error(t, err, "an undated reservation would hold the name forever")
	assert.Contains(t, err.Error(), "sites_reserved_has_deadline")
}

func TestMigrate_SitesRejectsAnUnknownState(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	_ = store

	_, err := repo.pool.Exec(ctx,
		`UPDATE sites SET state = 'wat' WHERE slug = $1`, reservationSlug)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sites_state_known")
}

func TestRegistryStore_UndeleteRefusesAReservationPastItsDeadline(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	_, err = store.Undelete(ctx, reservationSlug)
	assert.ErrorIs(t, err, registry.ErrNotFound,
		"an expired reservation belongs to the sweep; restoring it races the reclaim of its bytes")

	site, err := store.GetSite(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, site.IsReserved(), "the refused undelete leaves the row for the sweep")
}

func TestRegistryStore_ReleaseReservationDeletesAnExpiredReservedRow(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	require.NoError(t, store.ReleaseReservation(ctx, reservationSlug))

	_, err = store.GetSite(ctx, reservationSlug)
	assert.ErrorIs(t, err, registry.ErrNotFound)
}

func TestRegistryStore_ReleaseReservationRefusesARowInsideItsGraceWindow(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	assert.ErrorIs(t, store.ReleaseReservation(ctx, reservationSlug), registry.ErrNotFound)

	site, err := store.GetSite(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, site.IsReserved(), "a name still inside its grace window is not the sweep's to free")
}

func TestRegistryStore_ReleaseReservationRefusesARowThatIsNoLongerReserved(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	assert.ErrorIs(t, store.ReleaseReservation(ctx, reservationSlug), registry.ErrNotFound,
		"an unguarded delete here would drop a live site the sweep only ever saw as reserved")

	site, err := store.GetSite(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, site.IsReserved())
}

func TestRegistryStore_ReleaseReservationNowFreesAReservationBeforeItsDeadline(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname,
		time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	require.NoError(t, store.ReleaseReservationNow(ctx, reservationSlug),
		"the whole point of an approver release is that the deadline has not passed")

	_, err = store.GetSite(ctx, reservationSlug)
	assert.ErrorIs(t, err, registry.ErrNotFound, "the name must be registrable again")
}

func TestRegistryStore_ReleaseReservationNowRefusesAnActiveRow(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	err := store.ReleaseReservationNow(ctx, reservationSlug)

	assert.ErrorIs(t, err, registry.ErrNotFound,
		"releasing a live site would free a name whose owner never asked for a delete")
	site, getErr := store.GetSite(ctx, reservationSlug)
	require.NoError(t, getErr)
	assert.False(t, site.IsReserved())
}

func TestRegistryStore_ReleaseReservationNowIsNotFoundForAnAbsentSlug(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	assert.ErrorIs(t, store.ReleaseReservationNow(ctx, "absent"), registry.ErrNotFound)
}

func TestRegistryStore_ReservePrefersTheObservedAliasOverTheTable(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	live := "20260827-140000-newsha"

	res, err := store.Reserve(ctx, reservationSlug, reservationDirname,
		time.Now().UTC().Add(72*time.Hour), "bob",
		registry.ObservedAliases{Production: &live})
	require.NoError(t, err)

	assert.Equal(t, live, res.PrevProduction,
		"a promote that wrote R2 and then failed its aliases-table write leaves the table stale; undelete must republish what the edge actually served")
	assert.Equal(t, "20260102-000000-bbbbbbb", res.PrevPreview,
		"a mode the delete could not read must fall back to the table, not be blanked")
}
