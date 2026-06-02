package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/pg"
)

type fakeSource struct {
	events    []pg.OutboxEvent
	published map[int64]bool
}

func newFakeSource(topics ...string) *fakeSource {
	s := &fakeSource{published: map[int64]bool{}}
	for i, tp := range topics {
		s.events = append(s.events, pg.OutboxEvent{ID: int64(i + 1), Topic: tp, Payload: []byte(`{}`)})
	}
	return s
}

func (s *fakeSource) FetchUnpublished(_ context.Context, limit int) ([]pg.OutboxEvent, error) {
	var out []pg.OutboxEvent
	for _, e := range s.events {
		if !s.published[e.ID] {
			out = append(out, e)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *fakeSource) MarkPublished(_ context.Context, ids []int64, _ time.Time) error {
	for _, id := range ids {
		s.published[id] = true
	}
	return nil
}

type fakePublisher struct {
	got     []string
	failOn  int
	calls   int
	failErr error
}

func (p *fakePublisher) Publish(_ context.Context, topic string, _ []byte) error {
	p.calls++
	if p.failOn != 0 && p.calls == p.failOn {
		return p.failErr
	}
	p.got = append(p.got, topic)
	return nil
}

func TestOutboxRelay(t *testing.T) {
	src := newFakeSource("site.changed", "site.changed", "site.changed")
	pub := &fakePublisher{}
	relay := &Relay{Source: src, Publisher: pub, Now: func() time.Time { return time.Unix(0, 0) }}

	n, err := relay.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Len(t, pub.got, 3, "all events published")
	assert.True(t, src.published[1] && src.published[2] && src.published[3], "all marked published")

	n, err = relay.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n, "second pass finds nothing unpublished")
}

func TestOutboxRelay_StopsAtFailurePreservingOrder(t *testing.T) {
	src := newFakeSource("a", "b", "c")
	pub := &fakePublisher{failOn: 2, failErr: errors.New("engine down")}
	relay := &Relay{Source: src, Publisher: pub, Now: func() time.Time { return time.Unix(0, 0) }}

	n, err := relay.RunOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, 1, n, "only the first event published before the failure")
	assert.True(t, src.published[1], "succeeded event marked")
	assert.False(t, src.published[2], "failed event NOT marked -> retried next tick")
	assert.False(t, src.published[3], "later events not published out of order")
}

func TestOutboxRelay_AtLeastOnceOnMarkFailure(t *testing.T) {
	src := &markFailSource{fakeSource: newFakeSource("a"), failMark: true}
	pub := &fakePublisher{}
	relay := &Relay{Source: src, Publisher: pub}

	_, err := relay.RunOnce(context.Background())
	require.Error(t, err, "mark failure surfaces")
	assert.Len(t, pub.got, 1, "event was published")

	src.failMark = false
	_, err = relay.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Len(t, pub.got, 2, "unmarked event re-published (at-least-once; consumer must be idempotent, E1)")
}

type markFailSource struct {
	*fakeSource
	failMark bool
}

func (s *markFailSource) MarkPublished(ctx context.Context, ids []int64, at time.Time) error {
	if s.failMark {
		return errors.New("db down")
	}
	return s.fakeSource.MarkPublished(ctx, ids, at)
}
