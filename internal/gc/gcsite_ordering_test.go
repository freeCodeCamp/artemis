package gc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type orderRecorder struct {
	*fakeStore
	mover *fakeMover
	order []string
}

func (r *orderRecorder) Tombstone(ctx context.Context, site string, d Deploy) error {
	r.order = append(r.order, "row")
	return r.fakeStore.Tombstone(ctx, site, d)
}

func (r *orderRecorder) MovePrefix(ctx context.Context, src, dst string) (int, error) {
	r.order = append(r.order, "bytes")
	return r.mover.MovePrefix(ctx, src, dst)
}

func TestSiteGC_RecordsTheTombstoneRowBeforeMovingBytes(t *testing.T) {
	rec := &orderRecorder{
		fakeStore: &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}},
		mover:     &fakeMover{},
	}
	g := newSiteGC(rec, rec)

	res, err := g.Run(context.Background(), "www", false)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tombstoned)

	require.NotEmpty(t, rec.order)
	assert.Equal(t, "row", rec.order[0],
		"a crash between the two writes must leave a tombstone for bytes still in place (self-healing on "+
			"retry), never bytes in _trash/ that no tombstone dates — those are invisible to tombstone-purge "+
			"and to the index, so they are never hard-deleted")
}

type rowFailStore struct {
	*fakeStore
}

func (s *rowFailStore) Tombstone(context.Context, string, Deploy) error {
	return errors.New("pg down")
}

func TestSiteGC_LeavesBytesInPlaceWhenTheTombstoneRowFails(t *testing.T) {
	store := &rowFailStore{fakeStore: &fakeStore{deploys: map[string][]Deploy{"www": sixOld()}}}
	mover := &fakeMover{}
	g := newSiteGC(store, mover)

	_, err := g.Run(context.Background(), "www", false)
	require.Error(t, err)

	assert.Empty(t, mover.moves,
		"bytes must not move once the row that would date them is known to have failed")
}
