//go:build integration

package hatchet_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

type memOutbox struct {
	mu        sync.Mutex
	events    []pg.OutboxEvent
	published map[int64]bool
}

func newMemOutbox(events []pg.OutboxEvent) *memOutbox {
	return &memOutbox{events: events, published: map[int64]bool{}}
}

func (m *memOutbox) RelayBatch(_ context.Context, limit int, publish func(pg.OutboxEvent) error, _ time.Time) (int, error) {
	m.mu.Lock()
	var batch []pg.OutboxEvent
	for _, e := range m.events {
		if m.published[e.ID] {
			continue
		}
		if len(batch) >= limit {
			break
		}
		batch = append(batch, e)
	}
	m.mu.Unlock()

	done := 0
	for _, e := range batch {
		if err := publish(e); err != nil {
			return done, err
		}
		m.mu.Lock()
		m.published[e.ID] = true
		m.mu.Unlock()
		done++
	}
	return done, nil
}

func (m *memOutbox) outstanding() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.events {
		if !m.published[e.ID] {
			n++
		}
	}
	return n
}

func TestR5OutboxRelayAtLeastOnceAcrossRestart(t *testing.T) {
	obs := newObserver()
	h := startHarness(t, obs, map[string]worker.Handler{
		worker.WorkflowFinalize: instrumented(obs, 0, nil),
	})

	const n = 6
	sites := make([]string, n)
	events := make([]pg.OutboxEvent, n)
	for i := 0; i < n; i++ {
		sites[i] = fmt.Sprintf("r5-relay-%02d", i)
		events[i] = pg.OutboxEvent{
			ID:      int64(i + 1),
			Topic:   worker.WorkflowFinalize,
			Payload: []byte(fmt.Sprintf(`{"site":%q}`, sites[i])),
		}
	}

	src := newMemOutbox(events)
	relay := &worker.Relay{Source: src, Publisher: h.pub, Batch: 2}

	half := func() { _, _ = relay.RunOnce(context.Background()) }
	half()

	restartEngine(t)

	deadline := time.Now().Add(runReadyTimeout)
	for src.outstanding() > 0 && time.Now().Before(deadline) {
		if _, err := relay.RunOnce(context.Background()); err != nil {
			time.Sleep(pollInterval)
		}
	}
	require.Zero(t, src.outstanding(), "relay must drain the outbox after engine recovers")

	for _, site := range sites {
		h.waitStarts(t, site, 1)
		require.GreaterOrEqual(t, obs.startsFor(site), 1,
			"site %s must be delivered at least once across the restart", site)
	}
}

func restartEngine(t *testing.T) {
	t.Helper()
	composeFile := os.Getenv("HATCHET_COMPOSE_FILE")
	if composeFile == "" {
		t.Skip("HATCHET_COMPOSE_FILE unset; across-restart invariant not exercised without the compose stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "restart", "hatchet-lite")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "restart hatchet-lite: %s", string(out))
	time.Sleep(3 * time.Second)
}
