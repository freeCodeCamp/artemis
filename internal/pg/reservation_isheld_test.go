package pg

import (
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryStore_IsHeldOnlyWhileTheGraceIsUnexpired(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held, "an active site is not held; a guard that says otherwise stops all collection fleet-wide")

	_, err = store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	held, err = store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, held,
		"gc-site consults this before collecting; a false answer here trashes the deploys undelete exists to restore")
}

func TestRegistryStore_IsHeldGoesFalseOnceTheGraceHasPassed(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held,
		"an expired reservation must stop protecting the bytes, or the sweep can never reclaim the name")
}

func TestRegistryStore_IsHeldIsFalseForASlugThatDoesNotExist(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	held, err := store.IsHeld(ctx, "never-registered")
	require.NoError(t, err)
	assert.False(t, held, "an unknown slug is not held, and must not be an error that fails gc closed forever")
}

func TestRegistryStore_IsHeldGoesFalseAfterUndelete(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	_, err = store.Undelete(ctx, reservationSlug)
	require.NoError(t, err)

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held,
		"the guard and the restore path read the same predicate; if they disagree a restored site is never collected again")
}

func TestRegistryStore_IsExpiredReservationIsFalseInsideTheGrace(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	expired, err := store.IsExpiredReservation(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, expired, "a name still inside its grace is not the sweep's to trash")
}

func TestRegistryStore_IsExpiredReservationIsTruePastTheGrace(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(-time.Minute), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	expired, err := store.IsExpiredReservation(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, expired,
		"the sweep only proceeds on a positive claim; if this never goes true no name is ever reclaimed")
}

func TestRegistryStore_IsExpiredReservationIsFalseForASlugThatDoesNotExist(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	expired, err := store.IsExpiredReservation(ctx, "never-registered")
	require.NoError(t, err)
	assert.False(t, expired,
		"the sweep selected the row minutes earlier; if the row is gone its bytes belong to whoever holds the name now")
}

func TestRegistryStore_IsExpiredReservationIsFalseAfterUndelete(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	_, err = store.Undelete(ctx, reservationSlug)
	require.NoError(t, err)

	expired, err := store.IsExpiredReservation(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, expired,
		"an undeleted site is live; IsHeld also answers false for it, which is why the sweep cannot use IsHeld as its permission to destroy")
}

func TestRegistryStore_ExpireReservationMakesUndeleteRefuseButKeepsTheName(t *testing.T) {
	store, _, ctx := newReservationFixture(t)
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob", registry.ObservedAliases{})
	require.NoError(t, err)

	require.NoError(t, store.ExpireReservation(ctx, reservationSlug))

	_, err = store.Undelete(ctx, reservationSlug)
	require.Error(t, err,
		"SiteRelease expires the row before it moves any bytes; an undelete after a failed release restores alias pins onto bytes already in _trash")

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held)

	expired, err := store.IsExpiredReservation(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, expired,
		"the row must degrade to the sweep's shape so a half-finished release is completed by the next nightly run")
}

func TestRegistryStore_ExpireReservationIsNotFoundForAnActiveSite(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	require.ErrorIs(t, store.ExpireReservation(ctx, reservationSlug), registry.ErrNotFound,
		"expiring a live site would hand it to the sweep")
}

func TestRegistryStore_IsExpiredReservationIgnoresAPastDeadlineOnAnActiveRow(t *testing.T) {
	store, repo, ctx := newReservationFixture(t)

	_, err := repo.pool.Exec(ctx,
		`UPDATE sites SET reserved_until = now() - interval '1 hour' WHERE slug = $1`,
		reservationSlug)
	require.NoError(t, err)

	expired, err := store.IsExpiredReservation(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, expired,
		"the state clause is the only thing standing between an operator's hand-repaired row and the sweep trashing a live site")
}
