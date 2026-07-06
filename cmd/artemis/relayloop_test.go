package main

import (
	"context"
	"errors"
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

func (f *fakeOutbox) RelayBatch(_ context.Context, limit int, publish func(pg.OutboxEvent) error, _ time.Time) (int, error) {
	f.mu.Lock()
	batch := f.events
	if len(batch) > limit {
		batch = batch[:limit]
	}
	f.mu.Unlock()

	done := 0
	for _, e := range batch {
		if err := publish(e); err != nil {
			return done, err
		}
		done++
	}

	f.mu.Lock()
	for _, e := range batch {
		f.marked = append(f.marked, e.ID)
	}
	f.events = nil
	f.mu.Unlock()
	return done, nil
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

type erroringOutbox struct{}

func (erroringOutbox) RelayBatch(context.Context, int, func(pg.OutboxEvent) error, time.Time) (int, error) {
	return 0, errors.New("db down")
}

func TestRelayLoop_FailedTickBumpsFailures(t *testing.T) {
	relay := &worker.Relay{Source: erroringOutbox{}, Publisher: &fakePublisher{}, Batch: 10, Now: time.Now}
	metrics := worker.NewMetrics(prometheus.NewRegistry())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runRelayLoop(ctx, relay, time.Millisecond, metrics); close(done) }()

	require.Eventually(t, func() bool { return testutil.ToFloat64(metrics.RelayFailures) >= 1 }, 2*time.Second, time.Millisecond,
		"a relay RunOnce error must bump RelayFailures so a stalled outbox alerts")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runRelayLoop must return when ctx is cancelled even after error ticks")
	}
}
