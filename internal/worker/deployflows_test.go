package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployWorkflowsRegisterWithSiteKey(t *testing.T) {
	eng := &fakeEngine{}
	rt := NewRuntime(eng)

	require.NoError(t, RegisterDeployWorkflows(rt, noop, noop, noop))

	byName := map[string]WorkflowDef{}
	for _, d := range eng.registered {
		byName[d.Name] = d
	}
	require.Len(t, eng.registered, 3)
	for _, name := range []string{WorkflowFinalize, WorkflowPromote, WorkflowRollback} {
		assert.Equal(t, ConcurrencyKeySite, byName[name].ConcurrencyKey,
			"%s must serialize per-site via concurrency key (V8 single-writer-per-site)", name)
	}
}

func TestRegisterDeployWorkflows_PropagatesError(t *testing.T) {
	rt := NewRuntime(&fakeEngine{})
	require.NoError(t, RegisterDeployWorkflows(rt, noop, noop, noop))
	err := RegisterDeployWorkflows(rt, noop, noop, noop)
	require.Error(t, err, "re-registering the same workflow names is rejected")
}
