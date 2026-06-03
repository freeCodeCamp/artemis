package hatchet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/worker"
)

func TestAdapterRegister(t *testing.T) {
	a := New(Config{Token: "tok", Addr: "hatchet.svc:7077"})

	noop := func(context.Context, map[string]any) error { return nil }
	require.NoError(t, a.Register(worker.WorkflowDef{
		Name: worker.WorkflowGCSite, ConcurrencyKey: worker.ConcurrencyKeySite, Handler: noop,
	}))
	require.NoError(t, a.Register(worker.WorkflowDef{
		Name: worker.WorkflowTombstonePurge, Handler: noop,
	}))
	require.Len(t, a.Registered(), 2)

	require.Error(t, a.Register(worker.WorkflowDef{Name: "", Handler: noop}), "empty name rejected")
	require.Error(t, a.Register(worker.WorkflowDef{Name: "x"}), "nil handler rejected")
	require.Len(t, a.Registered(), 2, "rejected defs must not accumulate")
}

func TestAdapterPublishBeforeStart(t *testing.T) {
	a := New(Config{Token: "tok"})
	err := a.Publish(context.Background(), "site.changed", []byte(`{"site":"www.freecode.camp"}`))
	require.Error(t, err, "publish before Start must fail, not panic on a nil client")
}

func TestAdapterConnectBadAddr(t *testing.T) {
	a := New(Config{Token: "tok", Addr: "no-port"})
	err := a.Start(context.Background())
	require.Error(t, err)
}
