package gc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

func TestSiteGC_CollectsNothingWhileTheNameIsHeld(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)
	g.Held = func(context.Context, sitekey.Dirname) (bool, error) { return true, nil }

	res, err := g.Run(context.Background(), "www", false)

	require.NoError(t, err)
	assert.True(t, res.Held, "the verdict must say why nothing was collected")
	assert.Empty(t, res.Planned,
		"delete clears the alias rows that pin a deploy, so an unguarded gc-site trashes the very bytes undelete restores")
	assert.Empty(t, res.Tombstoned)
	assert.Empty(t, mover.moves, "not one byte may move while the name is inside its 72h grace")
	assert.Empty(t, store.tombstoned)
}

func TestSiteGC_CollectsNormallyWhenTheNameIsNotHeld(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	g := newSiteGC(store, &fakeMover{})
	g.Held = func(context.Context, sitekey.Dirname) (bool, error) { return false, nil }

	res, err := g.Run(context.Background(), "www", false)

	require.NoError(t, err)
	assert.False(t, res.Held)
	assert.NotEmpty(t, res.Tombstoned, "the guard must not disable collection for every site")
}

func TestSiteGC_RefusesToCollectWhenTheReservationCheckFails(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)
	g.Held = func(context.Context, sitekey.Dirname) (bool, error) {
		return false, errors.New("pg down")
	}

	res, err := g.Run(context.Background(), "www", false)

	require.Error(t, err, "an unreadable reservation state must fail closed; collecting on a failed check is the data-loss direction")
	assert.Empty(t, res.Tombstoned)
	assert.Empty(t, mover.moves)
}

func TestSiteGC_DryRunStillReportsAHeldName(t *testing.T) {
	store := &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}, targetsSeq: []map[string]struct{}{{}}}
	g := newSiteGC(store, &fakeMover{})
	g.Held = func(context.Context, sitekey.Dirname) (bool, error) { return true, nil }

	res, err := g.Run(context.Background(), "www", true)

	require.NoError(t, err)
	assert.True(t, res.Held)
	assert.Empty(t, res.Planned, "a dry run that plans deletes for a held site invites an operator to run them")
}
