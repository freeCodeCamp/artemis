//go:build integration

package hatchet_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	hatchetadapter "github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

const (
	harnessWorkflowA = "artemis.it.workflow-a"
	harnessWorkflowB = "artemis.it.workflow-b"
	harnessWorkflowC = "artemis.it.workflow-c"
)

type rendezvous struct {
	mu      sync.Mutex
	arrived int
	want    int
	ready   chan struct{}
	once    sync.Once
}

func newRendezvous(want int) *rendezvous {
	return &rendezvous{want: want, ready: make(chan struct{})}
}

func (r *rendezvous) arrive(ctx context.Context, hold time.Duration) {
	r.mu.Lock()
	r.arrived++
	reached := r.arrived >= r.want
	r.mu.Unlock()
	if reached {
		r.once.Do(func() { close(r.ready) })
	}
	select {
	case <-r.ready:
	case <-ctx.Done():
	case <-time.After(hold):
	}
}

func rendezvousHandler(obs *observer, rv *rendezvous, hold time.Duration) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		site := siteOf(input)
		obs.enter(site)
		defer obs.leave(site)
		rv.arrive(ctx, hold)
		return nil
	}
}

const (
	pollInterval       = 250 * time.Millisecond
	startupTimeout     = 30 * time.Second
	runReadyTimeout    = 180 * time.Second
	distinctSitesHold  = 60 * time.Second
	engineReadyTimeout = 300 * time.Second
)

const skipUsage = `
real-Hatchet integration suite skipped: %s not set.

To run against a live engine:

  cd test/integration/hatchet
  docker compose -f compose.hatchet.yaml up -d
  TOKEN=$(docker compose -f compose.hatchet.yaml exec -T hatchet-lite \
    /hatchet-admin token create --config /config \
    --tenant-id 707d0855-80ab-4e1f-a156-f1c4546cbf52 | tr -d '\r\n')
  HATCHET_CLIENT_TOKEN="$TOKEN" \
    HATCHET_CLIENT_HOST_PORT=127.0.0.1:7077 \
    HATCHET_CLIENT_TLS_STRATEGY=none \
    go test -tags=integration -count=1 -timeout=20m ./internal/hatchet/...
`

type harness struct {
	pub      worker.Publisher
	observed *observer
	suffix   string
	startErr <-chan error
}

func (h *harness) workerDied() error {
	select {
	case err := <-h.startErr:
		return err
	default:
		return nil
	}
}

type observer struct {
	mu           sync.Mutex
	starts       map[string]int
	active       map[string]int
	maxCo        map[string]int
	globalActive int
	globalMax    int
}

func newObserver() *observer {
	return &observer{
		starts: map[string]int{},
		active: map[string]int{},
		maxCo:  map[string]int{},
	}
}

func (o *observer) enter(site string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.starts[site]++
	o.active[site]++
	if o.active[site] > o.maxCo[site] {
		o.maxCo[site] = o.active[site]
	}
	o.globalActive++
	if o.globalActive > o.globalMax {
		o.globalMax = o.globalActive
	}
}

func (o *observer) leave(site string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active[site]--
	o.globalActive--
}

func (o *observer) peakGlobalConcurrency() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.globalMax
}

func (o *observer) startsFor(site string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.starts[site]
}

func (o *observer) peakConcurrency(site string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.maxCo[site]
}

func requireEngine(t *testing.T) {
	t.Helper()
	if os.Getenv("HATCHET_CLIENT_TOKEN") == "" {
		t.Skipf(skipUsage, "HATCHET_CLIENT_TOKEN")
	}
	if os.Getenv("HATCHET_CLIENT_HOST_PORT") == "" {
		t.Skipf(skipUsage, "HATCHET_CLIENT_HOST_PORT")
	}
}

func siteOf(input map[string]any) string {
	if v, ok := input[worker.ConcurrencyKeySite].(string); ok {
		return v
	}
	return ""
}

func startHarness(t *testing.T, obs *observer, handlers map[string]worker.Handler) *harness {
	t.Helper()
	requireEngine(t)

	suffix := shortID()
	adapter := hatchetadapter.New(hatchetadapter.Config{
		WorkerName: "artemis-it-" + suffix,
	})

	for _, def := range harnessDefs(obs, handlers, suffix) {
		require.NoError(t, adapter.Register(def))
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- adapter.Start(ctx) }()
	h := &harness{pub: adapter, observed: obs, suffix: suffix, startErr: errCh}

	waitPublishable(t, adapter)

	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
		}
	})

	return h
}

func harnessDefs(obs *observer, handlers map[string]worker.Handler, suffix string) []worker.WorkflowDef {
	names := []string{harnessWorkflowA, harnessWorkflowB, harnessWorkflowC}
	defs := make([]worker.WorkflowDef, 0, len(names))
	for _, name := range names {
		h := handlers[name]
		if h == nil {
			h = instrumented(obs, 0, nil)
		}
		scoped := scopedTopic(name, suffix)
		defs = append(defs, worker.WorkflowDef{
			Name:           scoped,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{scoped},
			Handler:        h,
		})
	}
	return defs
}

func scopedTopic(name, suffix string) string { return name + "." + suffix }

func instrumented(obs *observer, hold time.Duration, fail error) worker.Handler {
	return func(ctx context.Context, input map[string]any) error {
		site := siteOf(input)
		obs.enter(site)
		defer obs.leave(site)
		if hold > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(hold):
			}
		}
		return fail
	}
}

func waitPublishable(t *testing.T, pub worker.Publisher) {
	t.Helper()
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		err := pub.Publish(context.Background(), "artemis.it.warmup", []byte(`{"site":"__warmup__"}`))
		if err == nil {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("worker did not become publishable within %s", startupTimeout)
}

func (h *harness) fire(t *testing.T, topic, site string) {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"site":%q}`, site))
	require.NoError(t, h.pub.Publish(context.Background(), scopedTopic(topic, h.suffix), payload))
}

func (h *harness) waitStarts(t *testing.T, site string, want int, why string) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(runReadyTimeout)
	for time.Now().Before(deadline) {
		if h.observed.startsFor(site) >= want {
			return
		}
		time.Sleep(pollInterval)
	}
	if err := h.workerDied(); err != nil {
		t.Fatalf("site=%s: got %d starts, want >= %d after %s; the worker exited during the wait, which is the actual cause: %v",
			site, h.observed.startsFor(site), want, time.Since(started).Round(time.Second), err)
	}
	t.Fatalf("site=%s: got %d starts, want >= %d after %s; peak concurrency seen for this site was %d: %s",
		site, h.observed.startsFor(site), want, time.Since(started).Round(time.Second),
		h.observed.peakConcurrency(site), why)
}

func shortID() string {
	return uuid.NewString()[:8]
}
