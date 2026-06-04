package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type fakeOutbox struct {
	mu     sync.Mutex
	events []pg.OutboxEvent
	marked []int64
}

func (f *fakeOutbox) FetchUnpublished(_ context.Context, limit int) ([]pg.OutboxEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pg.OutboxEvent, 0, len(f.events))
	for _, e := range f.events {
		if len(out) >= limit {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeOutbox) MarkPublished(_ context.Context, ids []int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, ids...)
	f.events = nil
	return nil
}

type fakePublisher struct {
	mu sync.Mutex
	n  int
}

func (p *fakePublisher) Publish(context.Context, string, []byte) error {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	return nil
}

func (p *fakePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func TestRelayLoop(t *testing.T) {
	src := &fakeOutbox{events: []pg.OutboxEvent{
		{ID: 1, Topic: pg.TopicSiteChanged, Payload: []byte(`{"site":"www.freecode.camp"}`)},
	}}
	pub := &fakePublisher{}
	relay := &worker.Relay{Source: src, Publisher: pub, Batch: 10, Now: time.Now}
	metrics := worker.NewMetrics(prometheus.NewRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runRelayLoop(ctx, relay, time.Millisecond, metrics); close(done) }()

	require.Eventually(t, func() bool { return pub.count() >= 1 }, 2*time.Second, time.Millisecond,
		"relay loop must drain the outbox on tick")
	require.Eventually(t, func() bool { return testutil.ToFloat64(metrics.RelayPublished) >= 1 }, 2*time.Second, time.Millisecond,
		"relay loop must record published rows on /metrics")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runRelayLoop must return when ctx is cancelled")
	}
}
