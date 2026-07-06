package main

import (
	"testing"

	"github.com/freeCodeCamp/artemis/internal/handler"
	"github.com/freeCodeCamp/artemis/internal/pg"
	"github.com/stretchr/testify/require"
)

func TestWirePGRepo_WiresAuditAndAllPGDeps(t *testing.T) {
	h := &handler.Handlers{}
	wirePGRepo(h, nil)
	require.Nil(t, h.Audit, "nil repo wires nothing")

	wirePGRepo(h, &pg.Repo{})
	require.NotNil(t, h.Audit, "Audit MUST be wired or audit_log silently never persists for HTTP actions")
	require.NotNil(t, h.Outbox)
	require.NotNil(t, h.Tombstones)
	require.NotNil(t, h.Trash)
	require.NotNil(t, h.Index)
	require.NotNil(t, h.Locker)
}
