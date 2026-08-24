package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewWithRetryBudgetsEachAttempt(t *testing.T) {
	p := connectPolicy(45 * time.Second)

	require.Equal(t, ConnectTimeout, p.Attempt,
		"a zero attempt budget hands the whole window to one connect; a hung connect then retries nothing")
	require.Equal(t, "pg.connect.retrying", p.Event)
	require.Equal(t, 45*time.Second, p.Window)
	require.Equal(t, retryBackoffBase, p.Base)
	require.Equal(t, retryBackoffMax, p.Max)
}

func TestNewWithRetryUnreachableBoundedByWindow(t *testing.T) {
	start := time.Now()
	db, err := NewWithRetry(context.Background(), Config{
		DatabaseURL: "postgres://artemis:x@127.0.0.1:1/artemis?sslmode=disable&connect_timeout=1",
	}, 1200*time.Millisecond)
	require.Nil(t, db)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 3*time.Second, "window is a hard ceiling")
}
