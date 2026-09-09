package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedWriteProber struct {
	mu    sync.Mutex
	keys  []string
	err   error
	calls int
}

func (p *scriptedWriteProber) PutObject(_ context.Context, key string, body io.Reader, _ string, _ int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.keys = append(p.keys, key)
	if _, err := io.ReadAll(body); err != nil {
		return err
	}
	return p.err
}

func TestRunWriteProbe_WritesOneFixedKey(t *testing.T) {
	p := &scriptedWriteProber{}

	runWriteProbe(context.Background(), p)
	runWriteProbe(context.Background(), p)

	assert.Equal(t, []string{writeProbeKey, writeProbeKey}, p.keys,
		"the probe overwrites one key, so a 15-minute cadence cannot accumulate objects in the bucket")
}

func TestRunWriteProbe_ReportsAWriteGrantThatIsGone(t *testing.T) {
	var captured []string
	restore := captureBackground
	captureBackground = func(op string, _ error) { captured = append(captured, op) }
	t.Cleanup(func() { captureBackground = restore })

	runWriteProbe(context.Background(), &scriptedWriteProber{err: errors.New("401 Unauthorized")})

	assert.Equal(t, []string{opWriteProbe}, captured,
		"readyz calls ListObjectsV2 only, so a read-only key leaves pod state and every alert green "+
			"until the next deploy fails; the probe is the only signal")
}

func TestRunWriteProbeLoop_StopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &scriptedWriteProber{}
	done := make(chan struct{})
	go func() {
		runWriteProbeLoop(ctx, p, time.Millisecond)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail(t, "the probe loop outlived its context; a leaked goroutine writes to r2 after shutdown")
	}
}

func TestRunWriteProbeLoop_ProbesOnTheTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &scriptedWriteProber{}
	go runWriteProbeLoop(ctx, p, time.Millisecond)

	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.calls > 0
	}, 5*time.Second, time.Millisecond,
		"a loop that ticks without probing burns the interval and still reports nothing when the write grant is gone")
}
