package telemetry_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/freeCodeCamp/artemis/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromContext_NilSafeFallback(t *testing.T) {
	t.Parallel()

	s := telemetry.FromContext(context.Background())
	require.NotNil(t, s, "FromContext must never return nil")
	assert.Empty(t, s.Actor())
	assert.Empty(t, s.ReqID)

	s.SetActor("nobody")
	assert.Equal(t, "nobody", s.Actor())
}

func TestNewContext_RoundTripsSamePointer(t *testing.T) {
	t.Parallel()

	sc := telemetry.New("req-1")
	ctx := telemetry.NewContext(context.Background(), sc)

	got := telemetry.FromContext(ctx)
	require.Same(t, sc, got, "FromContext must return the SAME pointer installed (shared mutable cell)")
}

func TestSetters_MutateThroughSharedPointer(t *testing.T) {
	t.Parallel()

	sc := telemetry.New("req-2")
	ctx := telemetry.NewContext(context.Background(), sc)

	telemetry.FromContext(ctx).SetActor("alice")
	telemetry.FromContext(ctx).SetAction("site.promote")
	telemetry.FromContext(ctx).SetResource("www", "d-9")
	telemetry.FromContext(ctx).SetOutcome("success")
	telemetry.FromContext(ctx).SetRoute("/api/site/{site}/promote")

	assert.Equal(t, "req-2", sc.ReqID)
	assert.Equal(t, "alice", sc.Actor())
	assert.Equal(t, "site.promote", sc.Action())
	assert.Equal(t, "www", sc.Site())
	assert.Equal(t, "d-9", sc.DeployID())
	assert.Equal(t, "success", sc.Outcome())
	assert.Equal(t, "/api/site/{site}/promote", sc.Route())
}

func TestSetters_ConcurrentNoRace(t *testing.T) {
	t.Parallel()

	sc := telemetry.New("req-3")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc.SetActor("a")
			sc.SetAction("x")
			sc.SetResource("s", "d")
			sc.SetOutcome("o")
			sc.SetRoute("/r")
			_ = sc.LogAttrs()
		}()
	}
	wg.Wait()
	assert.Equal(t, "a", sc.Actor())
}

func TestLogAttrs_OrderAndOmitEmpty(t *testing.T) {
	t.Parallel()

	sc := telemetry.New("req-4")
	sc.SetActor("alice")
	sc.SetAction("deploy.finalize")
	sc.SetResource("www", "d-9")
	sc.SetOutcome("success")
	sc.SetRoute("/api/deploy/{deployID}/finalize")

	attrs := sc.LogAttrs()
	keys := make([]string, len(attrs))
	for i, a := range attrs {
		keys[i] = a.Key
	}
	assert.Equal(t, []string{"reqID", "actor", "action", "site", "deployId", "outcome", "route"}, keys)

	empty := telemetry.New("req-5")
	got := empty.LogAttrs()
	require.Len(t, got, 1)
	assert.Equal(t, "reqID", got[0].Key)
	assert.Equal(t, slog.KindString, got[0].Value.Kind())
	assert.Equal(t, "req-5", got[0].Value.String())
}
