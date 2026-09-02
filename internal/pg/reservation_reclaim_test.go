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

const reclaimTestTTL = 23 * time.Hour

func expireReservation(t *testing.T, store *RegistryStore, ctx context.Context, slug sitekey.Slug, dirname sitekey.Dirname) {
	t.Helper()
	_, err := store.Reserve(ctx, slug, dirname, time.Now().UTC().Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
}

func setClaim(t *testing.T, repo *Repo, ctx context.Context, slug sitekey.Slug, at time.Time) {
	t.Helper()
	_, err := repo.pool.Exec(ctx, `UPDATE sites SET reclaim_started_at = $2 WHERE slug = $1`, slug, at)
	require.NoError(t, err)
}

func countAudit(t *testing.T, repo *Repo, ctx context.Context, action string) int {
	t.Helper()
	var n int
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n))
	return n
}

func TestMigrate_AddsTheReclaimClaimColumn(t *testing.T) {
	repo := newTestRepo(t)
	var n int
	require.NoError(t, repo.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns WHERE table_name = 'sites' AND column_name = 'reclaim_started_at'`).Scan(&n))
	assert.Equal(t, 1, n, "ADR-022: the durable claim is a nullable timestamp on the reserved row, not a new state")
}

func TestRegistryStore_ReclaimableReservationsSkipsARowClaimedInsideTheTTL(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	_, err := store.Register(ctx, "fresh-claim", []string{"staff"}, "alice")
	require.NoError(t, err)
	_, err = store.Register(ctx, "stale-claim", []string{"staff"}, "alice")
	require.NoError(t, err)
	_, err = store.Register(ctx, "livehold", []string{"staff"}, "alice")
	require.NoError(t, err)
	expireReservation(t, store, ctx, reservationSlug, reservationDirname)
	expireReservation(t, store, ctx, "fresh-claim", "fresh-claim.freecode.camp")
	expireReservation(t, store, ctx, "stale-claim", "stale-claim.freecode.camp")
	_, err = store.Reserve(ctx, "livehold", "livehold.freecode.camp", time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
	now := time.Now().UTC()
	setClaim(t, repo, ctx, "fresh-claim", now.Add(-time.Hour))
	setClaim(t, repo, ctx, "stale-claim", now.Add(-reclaimTestTTL-time.Hour))

	rows, err := store.ReclaimableReservations(ctx, now, reclaimTestTTL, 10)

	require.NoError(t, err)
	var slugs []sitekey.Slug
	for _, r := range rows {
		slugs = append(slugs, r.Slug)
	}
	assert.ElementsMatch(t, []sitekey.Slug{reservationSlug, "stale-claim"}, slugs,
		"a claim inside the TTL belongs to a run that may still be moving bytes; a claim older than the TTL is a crashed run and is retried once per night")
}

func TestRegistryStore_ReclaimableReservationsIsOldestFirstAndHonoursItsLimit(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	for _, slug := range []sitekey.Slug{"newer", "oldest"} {
		_, err := store.Register(ctx, slug, []string{"staff"}, "alice")
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	_, err := store.Reserve(ctx, "newer", "newer.freecode.camp", now.Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
	_, err = store.Reserve(ctx, "oldest", "oldest.freecode.camp", now.Add(-48*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
	_, err = store.Reserve(ctx, reservationSlug, reservationDirname, now.Add(-time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	rows, err := store.ReclaimableReservations(ctx, now, reclaimTestTTL, 2)

	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, sitekey.Slug("oldest"), rows[0].Slug,
		"a backlog above the limit drains oldest-first across nights; without the order a row could be skipped forever")
	assert.Equal(t, reservationSlug, rows[1].Slug)
}

func TestRegistryStore_ClaimReclaimWinsOnceAndKeepsTheRowReserved(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	expireReservation(t, store, ctx, reservationSlug, reservationDirname)

	won, err := store.ClaimReclaim(ctx, reservationSlug, reclaimTestTTL)
	require.NoError(t, err)
	assert.True(t, won)

	again, err := store.ClaimReclaim(ctx, reservationSlug, reclaimTestTTL)
	require.NoError(t, err)
	assert.False(t, again, "a duplicate event inside the TTL must lose the claim, or two runs move the same prefix")

	var state string
	var claimed *time.Time
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT state, reclaim_started_at FROM sites WHERE slug = $1`, reservationSlug).Scan(&state, &claimed))
	assert.Equal(t, registry.StateReserved, state, "the claim is a timestamp on the reserved row, not a new state")
	require.NotNil(t, claimed)
	assert.WithinDuration(t, time.Now(), *claimed, time.Minute)
}

func TestRegistryStore_ClaimReclaimRetakesAClaimOlderThanTheTTL(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	expireReservation(t, store, ctx, reservationSlug, reservationDirname)
	setClaim(t, repo, ctx, reservationSlug, time.Now().UTC().Add(-reclaimTestTTL-time.Hour))

	won, err := store.ClaimReclaim(ctx, reservationSlug, reclaimTestTTL)

	require.NoError(t, err)
	assert.True(t, won, "a crashed run's claim expires with the TTL so the next night retries the row")
}

func TestRegistryStore_ClaimReclaimRefusesAnUnexpiredOrAbsentRow(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	won, err := store.ClaimReclaim(ctx, reservationSlug, reclaimTestTTL)
	require.NoError(t, err)
	assert.False(t, won, "a reservation inside its grace is undeletable and must never be claimed")

	won, err = store.ClaimReclaim(ctx, "absent", reclaimTestTTL)
	require.NoError(t, err)
	assert.False(t, won, "a row already released is a lost claim, not an error")
}

func TestRegistryStore_ReleaseReservationAuditedCommitsTheDeleteAndTheAuditTogether(t *testing.T) {
	var changed []sitekey.Slug
	store, repo, ctx := newReservationFixture(t)
	store = store.WithOnChange(func(slug sitekey.Slug) { changed = append(changed, slug) })
	expireReservation(t, store, ctx, reservationSlug, reservationDirname)
	changed = nil
	event := AuditEvent{
		Actor: "system:gc", Action: "site.reclaim", Site: string(reservationDirname), Outcome: "success",
		Detail: map[string]any{"moved": 3, "tombstoned": true},
	}

	require.NoError(t, store.ReleaseReservationAudited(ctx, reservationSlug, event))

	var n int
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT count(*) FROM sites WHERE slug = $1`, reservationSlug).Scan(&n))
	assert.Zero(t, n, "the name is free")
	assert.Equal(t, 1, countAudit(t, repo, ctx, "site.reclaim"))
	assert.Equal(t, []sitekey.Slug{reservationSlug}, changed,
		"the cached sites snapshot must drop the slug after the delete commits, as ReleaseReservation does")

	err := store.ReleaseReservationAudited(ctx, reservationSlug, event)
	assert.ErrorIs(t, err, registry.ErrNotFound)
	assert.Equal(t, 1, countAudit(t, repo, ctx, "site.reclaim"),
		"a retry after the committed delete finds no row and writes no second audit row")
}

func TestRegistryStore_ReleaseReservationAuditedWritesNoAuditRowForAnUnexpiredRow(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	err = store.ReleaseReservationAudited(ctx, reservationSlug, AuditEvent{Actor: "system:gc", Action: "site.reclaim", Outcome: "success"})

	assert.ErrorIs(t, err, registry.ErrNotFound)
	assert.Zero(t, countAudit(t, repo, ctx, "site.reclaim"), "no audit row without the deleting transaction")
	var n int
	require.NoError(t, repo.pool.QueryRow(ctx, `SELECT count(*) FROM sites WHERE slug = $1`, reservationSlug).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestRegistryStore_UndeleteRefusesAClaimedRow(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)
	setClaim(t, repo, ctx, reservationSlug, time.Now().UTC())

	_, err = store.Undelete(ctx, reservationSlug)

	assert.ErrorIs(t, err, registry.ErrNotFound,
		"ADR-022: undelete refuses a row whose claim is set; a reclaim may already have moved its bytes")
}

func TestRecordAuditTx_RollsBackWithItsTransaction(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	tx, err := repo.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, RecordAuditTx(ctx, tx, AuditEvent{Actor: "system:gc", Action: "site.reclaim", Outcome: "success"}))
	require.NoError(t, tx.Rollback(ctx))

	assert.Zero(t, countAudit(t, repo, ctx, "site.reclaim"), "an audit row written on the transaction dies with it")
}
