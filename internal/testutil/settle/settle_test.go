package settle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func script(results ...any) (Observation, *int) {
	calls := 0
	return func(context.Context) (bool, error) {
		i := calls
		calls++
		if i >= len(results) {
			i = len(results) - 1
		}
		switch v := results[i].(type) {
		case bool:
			return v, nil
		case error:
			return false, v
		}
		return false, nil
	}, &calls
}

func TestUntil_FirstSuccessReturnsNil(t *testing.T) {
	obs, calls := script(true)
	require.NoError(t, Until(t.Context(), time.Second, obs, Every(time.Hour)))
	assert.Equal(t, 1, *calls)
}

func TestUntil_ConsecutiveCountsARun(t *testing.T) {
	obs, calls := script(true, true, false, true, true, true)
	require.NoError(t, Until(t.Context(), time.Second, obs, Every(time.Millisecond), Consecutive(3)))
	assert.Equal(t, 6, *calls, "a miss resets the streak; totals do not count")
}

func TestUntil_ErrorResetsTheStreak(t *testing.T) {
	obs, calls := script(true, true, errors.New("blip"), true, true, true)
	require.NoError(t, Until(t.Context(), time.Second, obs, Every(time.Millisecond), Consecutive(3)))
	assert.Equal(t, 6, *calls)
}

func TestUntil_CancelMidWaitReturnsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	obs, _ := script(false)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	err := Until(ctx, time.Minute, obs, Every(10*time.Second))
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second)
}

func TestUntil_NoObservationAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	obs, calls := script(true)
	err := Until(ctx, time.Second, obs)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, *calls)
}

func TestUntil_BudgetExpiryWrapsLastError(t *testing.T) {
	boom := errors.New("boom")
	obs, _ := script(boom)
	err := Until(t.Context(), 30*time.Millisecond, obs, Every(5*time.Millisecond))
	require.ErrorIs(t, err, ErrBudgetExpired)
	assert.ErrorIs(t, err, boom)
}

func TestUntil_BudgetExpiryWithoutErrorNamesBudgetAndAttempts(t *testing.T) {
	obs, calls := script(false)
	err := Until(t.Context(), 150*time.Millisecond, obs, Every(5*time.Millisecond))
	require.ErrorIs(t, err, ErrBudgetExpired)
	assert.Contains(t, err.Error(), "150ms")
	assert.Contains(t, err.Error(), "attempt")
	assert.Greater(t, *calls, 1)
}

func TestUntil_DeadlineIsCheckedBeforeNotAfter(t *testing.T) {
	obs := func(context.Context) (bool, error) {
		time.Sleep(60 * time.Millisecond)
		return true, nil
	}
	require.NoError(t, Until(t.Context(), 30*time.Millisecond, obs, Every(time.Millisecond)))
}

func TestUntil_ZeroBudgetIsOneObservation(t *testing.T) {
	obs, calls := script(false)
	err := Until(t.Context(), 0, obs, Every(time.Hour))
	require.ErrorIs(t, err, ErrBudgetExpired)
	assert.Equal(t, 1, *calls)
}

func TestUntil_PerAttemptBoundsOnlyTheObservation(t *testing.T) {
	calls := 0
	obs := func(ctx context.Context) (bool, error) {
		calls++
		if calls < 3 {
			<-ctx.Done()
			return false, ctx.Err()
		}
		return true, nil
	}
	require.NoError(t, Until(t.Context(), time.Second, obs, Every(time.Millisecond), PerAttempt(5*time.Millisecond)))
	assert.Equal(t, 3, calls, "a per-attempt timeout is one failed observation, not the end of the wait")
}

func TestUntil_ConsecutiveBelowOneIsOne(t *testing.T) {
	obs, calls := script(true)
	require.NoError(t, Until(t.Context(), time.Second, obs, Consecutive(0)))
	assert.Equal(t, 1, *calls)
}

func TestUntil_CancelKeepsTheLastObservationError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	boom := errors.New("boom")
	obs, _ := script(boom)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	err := Until(ctx, time.Minute, obs, Every(10*time.Second))
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, boom)
}

func TestUntil_ExpiryNamesOnlyTheLatestError(t *testing.T) {
	boom := errors.New("boom")
	obs, _ := script(boom, false)
	err := Until(t.Context(), 60*time.Millisecond, obs, Every(5*time.Millisecond))
	require.ErrorIs(t, err, ErrBudgetExpired)
	assert.NotErrorIs(t, err, boom, "a clean miss after a transient error must not blame the old error")
}
