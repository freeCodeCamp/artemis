package hatchet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func craftJWT(t *testing.T) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	claims := map[string]any{
		"server_url":             "https://hatchet.local",
		"grpc_broadcast_address": "localhost:7077",
		"exp":                    float64(time.Now().Add(time.Hour).Unix()),
		"sub":                    "707d0855-80ab-4e1f-a156-f1c4546cbf52",
	}
	return encodeSegment(t, header) + "." + encodeSegment(t, claims) + "." + "sig"
}

func encodeSegment(t *testing.T, v map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func encodeClaims(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	return encodeSegment(t, header) + "." + encodeSegment(t, claims) + "." + "sig"
}

func isolateClientEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	t.Setenv("HATCHET_CLIENT_TOKEN", "")
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "")
	t.Setenv("HATCHET_CLIENT_SERVER_URL", "")
	t.Setenv("HATCHET_CLIENT_TENANT_ID", "")
	t.Setenv("HATCHET_CLIENT_NAMESPACE", "")
}

func TestAdapterConnectPanicsOnMalformedToken(t *testing.T) {
	isolateClientEnv(t)

	missingServerURL := encodeClaims(t, map[string]any{
		"grpc_broadcast_address": "localhost:7077",
		"exp":                    float64(time.Now().Add(time.Hour).Unix()),
		"sub":                    "707d0855-80ab-4e1f-a156-f1c4546cbf52",
	})

	cases := []struct {
		name  string
		token string
	}{
		{"two-segment token", "not.ajwt"},
		{"bad base64 claims segment", "a.b.c"},
		{"missing server_url claim", missingServerURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Config{Token: tc.token})
			require.Panics(t, func() {
				client, err := a.connect()
				_ = client
				_ = err
			}, "connect() must panic (not return error) on a malformed token; Start()'s error-wrap is dead code for this path")
		})
	}
}

func TestAdapterBuildWorkflowConcurrencyExpression(t *testing.T) {
	isolateClientEnv(t)

	tok := craftJWT(t)
	a := New(Config{Token: tok, Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)

	noop := func(context.Context, map[string]any) error { return nil }
	wf := a.buildWorkflow(c, worker.WorkflowDef{
		Name:           worker.WorkflowGCSite,
		ConcurrencyKey: worker.ConcurrencyKeySite,
		Handler:        noop,
	})
	require.Equal(t, worker.WorkflowGCSite, wf.GetName())

	req, _, _, _ := wf.Dump()
	require.Len(t, req.ConcurrencyArr, 1, "exactly one concurrency option from a non-empty ConcurrencyKey")
	conc := req.ConcurrencyArr[0]
	require.Equal(t, "input.site", conc.GetExpression(),
		"per-site fan-out limiter must be input.<key>; a wrong prefix silently breaks per-site isolation")
	require.EqualValues(t, 1, conc.GetMaxRuns())
	require.Equal(t, "GROUP_ROUND_ROBIN", conc.GetLimitStrategy().String())
}

func TestAdapterBuildWorkflowOptionGuards(t *testing.T) {
	isolateClientEnv(t)

	tok := craftJWT(t)
	a := New(Config{Token: tok, Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)

	noop := func(context.Context, map[string]any) error { return nil }

	cases := []struct {
		name    string
		def     worker.WorkflowDef
		wantCo  bool
		events  []string
		crons   []string
		wantExp string
	}{
		{
			name: "empty config attaches no concurrency/events/cron",
			def: worker.WorkflowDef{
				Name:    worker.WorkflowTombstonePurge,
				Handler: noop,
			},
			wantCo: false,
			events: nil,
			crons:  nil,
		},
		{
			name: "full config attaches all three",
			def: worker.WorkflowDef{
				Name:           worker.WorkflowGCSite,
				ConcurrencyKey: worker.ConcurrencyKeySite,
				EventTriggers:  []string{"site.changed"},
				Cron:           []string{"0 0 * * *"},
				Handler:        noop,
			},
			wantCo:  true,
			events:  []string{"site.changed"},
			crons:   []string{"0 0 * * *"},
			wantExp: "input.site",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := a.buildWorkflow(c, tc.def)
			req, _, _, _ := wf.Dump()

			if tc.wantCo {
				require.Len(t, req.ConcurrencyArr, 1)
				require.Equal(t, tc.wantExp, req.ConcurrencyArr[0].GetExpression())
			} else {
				require.Empty(t, req.ConcurrencyArr,
					"empty ConcurrencyKey must not attach a zero-value concurrency option")
			}
			require.Equal(t, tc.events, req.GetEventTriggers())
			require.Equal(t, tc.crons, req.GetCronTriggers())
		})
	}
}

func TestAdapterPublishDecodeErrorPath(t *testing.T) {
	isolateClientEnv(t)

	tok := craftJWT(t)
	a := New(Config{Token: tok, Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)
	a.setClient(c)

	err = a.Publish(context.Background(), "site.changed", []byte(`{bad json`))
	require.Error(t, err, "malformed payload must surface a decode error, not push garbage to the event bus")
	require.ErrorContains(t, err, "decode payload")
}

func TestAdapterBuildWorkflow_CarriesTheExecutionTimeout(t *testing.T) {
	isolateClientEnv(t)

	a := New(Config{Token: craftJWT(t), Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)
	noop := func(context.Context, map[string]any) error { return nil }

	withTimeout := a.buildWorkflow(c, worker.WorkflowDef{
		Name:             "drift-detect",
		Cron:             []string{"0 4 * * *"},
		ExecutionTimeout: 15 * time.Minute,
		Handler:          noop,
	})
	req, _, _, _ := withTimeout.Dump()
	require.Len(t, req.GetTasks(), 1)
	require.Equal(t, "900s", req.GetTasks()[0].GetTimeout(),
		"a fleet-wide sweep runs for minutes; on the engine default it would be killed every night")

	bare := a.buildWorkflow(c, worker.WorkflowDef{Name: "gc-site", Handler: noop})
	bareReq, _, _, _ := bare.Dump()
	require.Len(t, bareReq.GetTasks(), 1)
	require.Empty(t, bareReq.GetTasks()[0].GetTimeout(),
		"an unset timeout must stay unset, so short workflows keep the engine default")
}

func TestAdapterPublishIsSafeWhileStartInstallsTheClient(t *testing.T) {
	isolateClientEnv(t)

	a := New(Config{Token: craftJWT(t), Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)

	const publishers = 8
	const rounds = 300
	var pushed atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})

	wg.Add(publishers + 1)
	go func() {
		defer wg.Done()
		<-gate
		for range rounds {
			a.setClient(c)
		}
	}()
	for range publishers {
		go func() {
			defer wg.Done()
			<-gate
			for range rounds {
				if a.Publish(context.Background(), "site.changed", []byte(`{bad json`)) == nil {
					pushed.Add(1)
				}
			}
		}()
	}
	close(gate)
	wg.Wait()

	require.Zero(t, pushed.Load(),
		"every call must fail at the decode step; a nil count would mean the loop reached the network and the run proves nothing")
}

func TestAdapterWorkerNameFallsBackToTheDefault(t *testing.T) {
	require.Equal(t, defaultWorkerName, New(Config{}).workerName(),
		"an unset WorkerName must still name the worker; the engine rejects an empty name")
	require.Equal(t, "artemis-canary", New(Config{WorkerName: "artemis-canary"}).workerName())
}

func TestAdapterStopIsANoOpBecauseShutdownIsContextCancellation(t *testing.T) {
	require.NoError(t, New(Config{}).Stop(context.Background()),
		"StartBlocking unwinds on rootCtx cancellation; a Stop that cleared state would only add a nil window")
}

func TestAdapterPublishSurfacesAFailedPush(t *testing.T) {
	isolateClientEnv(t)

	a := New(Config{Token: craftJWT(t), Addr: "localhost:7077"})
	c, err := a.connect()
	require.NoError(t, err)
	a.setClient(c)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = a.Publish(ctx, "site.changed", []byte(`{"site":"www"}`))
	require.Error(t, err,
		"a swallowed push marks the outbox row published while the engine never saw the event")
	require.ErrorContains(t, err, "publish site.changed")
}
