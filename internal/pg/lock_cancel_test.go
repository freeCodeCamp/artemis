package pg

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLockSession_CancelledAcquireUnwrapsToContextCanceled(t *testing.T) {
	repo := newTestRepo(t)
	live := context.Background()

	sess, err := repo.NewLockSession(live)
	require.NoError(t, err)
	defer sess.Close(live)

	cancelled, cancel := context.WithCancel(live)
	cancel()

	err = sess.WithSiteLock(cancelled, "cancel-probe.freecode.camp", func() error {
		t.Fatal("the closure must not run once acquisition fails")
		return nil
	})

	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled,
		"internal/handler/handler.go routes errors.Is(err, context.Canceled) to client.disconnect at "+
			"WARN and 499; an error that only reads 'context canceled' without unwrapping is reported "+
			"as a server fault and paged on")
}

func TestLockSession_CancelledMidWaitUnwrapsToContextCanceled(t *testing.T) {
	repo := newTestRepo(t)
	live := context.Background()
	const site = "cancel-midwait.freecode.camp"

	holder, err := repo.NewLockSession(live)
	require.NoError(t, err)
	waiter, err := repo.NewLockSession(live)
	require.NoError(t, err)
	defer waiter.Close(live)

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = holder.WithSiteLock(live, site, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	waitCtx, cancelWait := context.WithCancel(live)
	waitErr := make(chan error, 1)
	go func() {
		waitErr <- waiter.WithSiteLock(waitCtx, site, func() error {
			return nil
		})
	}()

	time.Sleep(300 * time.Millisecond)
	cancelWait()

	select {
	case err := <-waitErr:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled,
			"this is the production shape: a staff client hangs up while pg_advisory_lock is still "+
				"waiting, and the six historical Sentry rows carried exactly this error text")
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled waiter must not stay blocked on pg_advisory_lock")
	}

	close(release)
	<-holderDone
	holder.Close(live)
}
