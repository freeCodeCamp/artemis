package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runRelayLoop(ctx, relay, nil, time.Millisecond); close(done) }()

	require.Eventually(t, func() bool { return pub.count() >= 1 }, 2*time.Second, time.Millisecond,
		"relay loop must drain the outbox on tick")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runRelayLoop must return when ctx is cancelled")
	}
}

type erroringOutbox struct {
	mu sync.Mutex
	n  int
}

func (e *erroringOutbox) RelayBatch(context.Context, int, func(pg.OutboxEvent) error, time.Time) (int, error) {
	e.mu.Lock()
	e.n++
	e.mu.Unlock()
	return 0, errors.New("db down")
}

func (e *erroringOutbox) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

func TestRelayLoop_SurvivesFailedTicks(t *testing.T) {
	src := &erroringOutbox{}
	relay := &worker.Relay{Source: src, Publisher: &fakePublisher{}, Batch: 10, Now: time.Now}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runRelayLoop(ctx, relay, nil, time.Millisecond); close(done) }()

	require.Eventually(t, func() bool { return src.calls() >= 2 }, 2*time.Second, time.Millisecond,
		"a failed RunOnce must not kill the loop; it keeps ticking")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runRelayLoop must return when ctx is cancelled even after error ticks")
	}
}

type backlogReading struct {
	count  int
	oldest time.Duration
	err    error
}

type scriptedBacklog struct {
	mu      sync.Mutex
	script  []backlogReading
	nProbes int
}

func (b *scriptedBacklog) OutboxBacklog(context.Context) (int, time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.script[min(b.nProbes, len(b.script)-1)]
	b.nProbes++
	return r.count, r.oldest, r.err
}

func (b *scriptedBacklog) probes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nProbes
}

type capturedOps struct {
	mu  sync.Mutex
	ops []string
}

func (c *capturedOps) add(op string) {
	c.mu.Lock()
	c.ops = append(c.ops, op)
	c.mu.Unlock()
}

func (c *capturedOps) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

func trapCaptures(t *testing.T) *capturedOps {
	t.Helper()
	got := &capturedOps{}
	restore := captureBackground
	captureBackground = func(op string, _ error) { got.add(op) }
	t.Cleanup(func() { captureBackground = restore })
	return got
}

func watchBacklog(t *testing.T, src *scriptedBacklog) (*capturedOps, func()) {
	t.Helper()
	got := trapCaptures(t)
	relay := &worker.Relay{Source: &fakeOutbox{}, Publisher: &fakePublisher{}, Batch: 10, Now: time.Now}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runRelayLoop(ctx, relay, src, time.Millisecond); close(done) }()

	return got, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("runRelayLoop must return when ctx is cancelled")
		}
	}
}

func waitForProbes(t *testing.T, src *scriptedBacklog, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return src.probes() >= n }, 5*time.Second, time.Millisecond,
		"the loop must keep probing the backlog on its own cadence")
}

func TestRunRelayLoop_PagesOnceWhenTheBacklogStopsDraining(t *testing.T) {
	src := &scriptedBacklog{script: []backlogReading{{count: 40, oldest: outboxStuckAfter}}}
	got, stop := watchBacklog(t, src)
	defer stop()

	require.Eventually(t, func() bool { return len(got.snapshot()) == 1 }, 5*time.Second, time.Millisecond,
		"a relay that reports success while draining nothing must raise the backlog, not stay silent")
	require.Equal(t, []string{opOutboxBacklog}, got.snapshot())

	waitForProbes(t, src, src.probes()+3)

	require.Equal(t, []string{opOutboxBacklog}, got.snapshot(),
		"a backlog that stays stuck is one incident; re-paging every probe would bury it in its own noise")
}

func TestRunRelayLoop_IgnoresABacklogYoungerThanTheStuckThreshold(t *testing.T) {
	src := &scriptedBacklog{script: []backlogReading{{count: 500, oldest: outboxStuckAfter - time.Second}}}
	got, stop := watchBacklog(t, src)
	defer stop()

	waitForProbes(t, src, 3)

	require.Empty(t, got.snapshot(),
		"depth alone is a burst; the head must outlive claimTTL and DefaultRelayBatchTimeout before a "+
			"normal claim-expiry cycle can be told apart from a relay that has stopped draining")
}

func TestRunRelayLoop_PagesAgainAfterTheBacklogDrainsAndStalls(t *testing.T) {
	src := &scriptedBacklog{script: []backlogReading{
		{count: 40, oldest: outboxStuckAfter},
		{},
		{count: 7, oldest: outboxStuckAfter},
	}}
	got, stop := watchBacklog(t, src)
	defer stop()

	require.Eventually(t, func() bool { return len(got.snapshot()) == 2 }, 5*time.Second, time.Millisecond,
		"a backlog that drained and stalled again is a second incident, not an echo of the first")

	waitForProbes(t, src, src.probes()+3)

	require.Equal(t, []string{opOutboxBacklog, opOutboxBacklog}, got.snapshot())
}

func TestRunRelayLoop_DoesNotPageWhenTheBacklogIsUnreadable(t *testing.T) {
	src := &scriptedBacklog{script: []backlogReading{
		{count: 5, oldest: 30 * time.Minute, err: errors.New("pg down")},
	}}
	got, stop := watchBacklog(t, src)
	defer stop()

	waitForProbes(t, src, 3)

	require.Empty(t, got.snapshot(),
		"a query that failed vouches for none of the numbers beside it; an unreadable backlog is "+
			"unknown, and paging on it would fire every time Postgres blinks")
}
