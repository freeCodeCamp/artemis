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

	"github.com/hatchet-dev/hatchet/pkg/client/rest"

	hsdk "github.com/hatchet-dev/hatchet/sdks/go"

	hatchetadapter "github.com/freeCodeCamp/artemis/internal/hatchet"
	"github.com/freeCodeCamp/artemis/internal/worker"
)

const (
	siteKey         = "input.site"
	pollInterval    = 250 * time.Millisecond
	startupTimeout  = 30 * time.Second
	runReadyTimeout = 90 * time.Second
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
    go test -tags=integration -count=1 -timeout=10m ./internal/hatchet/...
`

type harness struct {
	pub      worker.Publisher
	client   *hsdk.Client
	observed *observer
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

	adapter := hatchetadapter.New(hatchetadapter.Config{
		WorkerName: "artemis-it-" + shortID(),
	})

	for _, def := range deployDefs(obs, handlers) {
		require.NoError(t, adapter.Register(def))
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- adapter.Start(ctx) }()

	waitPublishable(t, adapter)

	client, err := hsdk.NewClient()
	require.NoError(t, err)

	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
		}
	})

	return &harness{pub: adapter, client: client, observed: obs}
}

func deployDefs(obs *observer, handlers map[string]worker.Handler) []worker.WorkflowDef {
	names := []string{worker.WorkflowFinalize, worker.WorkflowPromote, worker.WorkflowRollback}
	defs := make([]worker.WorkflowDef, 0, len(names))
	for _, name := range names {
		h := handlers[name]
		if h == nil {
			h = instrumented(obs, 0, nil)
		}
		defs = append(defs, worker.WorkflowDef{
			Name:           name,
			ConcurrencyKey: worker.ConcurrencyKeySite,
			EventTriggers:  []string{name},
			Handler:        h,
		})
	}
	return defs
}

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
	require.NoError(t, h.pub.Publish(context.Background(), topic, payload))
}

func (h *harness) waitStarts(t *testing.T, site string, want int) {
	t.Helper()
	deadline := time.Now().Add(runReadyTimeout)
	for time.Now().Before(deadline) {
		if h.observed.startsFor(site) >= want {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("site=%s: got %d starts, want >= %d within %s",
		site, h.observed.startsFor(site), want, runReadyTimeout)
}

func (h *harness) waitRunStatus(t *testing.T, runID string, target rest.V1TaskStatus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runReadyTimeout)
	defer cancel()
	for {
		details, err := h.client.Runs().GetDetails(ctx, uuid.MustParse(runID))
		if err == nil {
			if details.Status == target {
				return
			}
			for _, tr := range details.TaskRuns {
				if tr.Status == target {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("run %s did not reach status %s within %s", runID, target, runReadyTimeout)
		case <-time.After(pollInterval):
		}
	}
}

func shortID() string {
	return uuid.NewString()[:8]
}
