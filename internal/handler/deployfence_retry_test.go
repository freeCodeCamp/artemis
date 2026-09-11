package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/freeCodeCamp/artemis/internal/sitekey"
)

type flakyDeployFence struct {
	failures int
	attempts int
	marked   bool
}

func (f *flakyDeployFence) MarkDeployFinalized(context.Context, sitekey.Slug, string, string, time.Duration) error {
	f.attempts++
	if f.attempts <= f.failures {
		return errors.New("dial tcp 10.43.0.1:6379: connect: connection refused")
	}
	f.marked = true
	return nil
}

func (f *flakyDeployFence) IsDeployFinalized(context.Context, sitekey.Slug, string) (bool, error) {
	return f.marked, nil
}

func (f *flakyDeployFence) IsDeployModeFinalized(context.Context, sitekey.Slug, string, string) (bool, error) {
	return f.marked, nil
}

func TestFenceFinalizedDeployRetriesATransientWriteFailure(t *testing.T) {
	fence := &flakyDeployFence{failures: indexCommitAttempts - 1}
	h := &Handlers{DeployFence: fence, DeployJWTTTL: time.Minute}

	h.fenceFinalizedDeploy(context.Background(), "www", "20260420-141522-abc1234", "preview")

	require.True(t, fence.marked, "the fence must be written after a transient failure")
	require.Equal(t, indexCommitAttempts, fence.attempts)
}
