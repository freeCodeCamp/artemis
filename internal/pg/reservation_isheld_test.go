package pg

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryStore_IsHeldOnlyWhileTheGraceIsUnexpired(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held, "an active site is not held; a guard that says otherwise stops all collection fleet-wide")

	_, err = store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob")
	require.NoError(t, err)

	held, err = store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.True(t, held,
		"gc-site consults this before collecting; a false answer here trashes the deploys undelete exists to restore")
}

func TestRegistryStore_IsHeldGoesFalseOnceTheGraceHasPassed(t *testing.T) {
	store, _, ctx := newReservationFixture(t)

	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(-time.Minute), "bob")
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
	_, err := store.Reserve(ctx, reservationSlug, reservationDirname, time.Now().UTC().Add(72*time.Hour), "bob")
	require.NoError(t, err)

	_, err = store.Undelete(ctx, reservationSlug)
	require.NoError(t, err)

	held, err := store.IsHeld(ctx, reservationSlug)
	require.NoError(t, err)
	assert.False(t, held,
		"the guard and the restore path read the same predicate; if they disagree a restored site is never collected again")
}
