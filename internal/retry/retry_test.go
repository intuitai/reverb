package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errTemporary = errors.New("temporary error")

func TestRetry_SucceedsImmediately(t *testing.T) {
	cfg := DefaultConfig()
	calls := 0

	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		calls++
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls, "fn should be called exactly once when it succeeds immediately")
}

func TestRetry_SucceedsAfterRetries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialDelay = 1 * time.Millisecond // speed up test
	calls := 0

	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return errTemporary
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 3, calls, "fn should be called 3 times: 2 failures then 1 success")
}

func TestRetry_ExhaustsRetries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.InitialDelay = 1 * time.Millisecond // speed up test
	calls := 0

	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		calls++
		return errTemporary
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, errTemporary)
	assert.Equal(t, cfg.MaxRetries+1, calls, "fn should be called MaxRetries+1 times (initial + retries)")
}

func TestRetry_ExponentialBackoff(t *testing.T) {
	cfg := Config{
		MaxRetries:   3,
		InitialDelay: 50 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		JitterFrac:   0.25,
	}

	var timestamps []time.Time

	err := Do(context.Background(), cfg, func(ctx context.Context) error {
		timestamps = append(timestamps, time.Now())
		return errTemporary
	})

	require.Error(t, err)
	require.Len(t, timestamps, 4, "should have 4 timestamps (initial + 3 retries)")

	// Expected base delays: 50ms, 100ms, 200ms
	expectedDelays := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}

	for i := 0; i < len(expectedDelays); i++ {
		actual := timestamps[i+1].Sub(timestamps[i])
		base := expectedDelays[i]
		low := time.Duration(float64(base) * 0.70)  // generous lower bound
		high := time.Duration(float64(base) * 1.50)  // generous upper bound

		assert.GreaterOrEqual(t, actual, low,
			"delay %d should be >= %.0f%% of base %v, got %v", i, 70.0, base, actual)
		assert.LessOrEqual(t, actual, high,
			"delay %d should be <= %.0f%% of base %v, got %v", i, 150.0, base, actual)
	}

	// Verify delays roughly double: each delay should be greater than the previous one
	for i := 1; i < len(expectedDelays); i++ {
		prev := timestamps[i].Sub(timestamps[i-1])
		curr := timestamps[i+1].Sub(timestamps[i])
		assert.Greater(t, curr.Milliseconds(), prev.Milliseconds(),
			"delay should increase: delay[%d]=%v should be > delay[%d]=%v", i, curr, i-1, prev)
	}
}

func TestRetry_ContextCancellation(t *testing.T) {
	cfg := Config{
		MaxRetries:   10,
		InitialDelay: 1 * time.Second, // long delay so cancellation triggers during wait
		MaxDelay:     10 * time.Second,
		JitterFrac:   0.25,
	}

	ctx, cancel := context.WithCancel(context.Background())
	var calls int32

	// Cancel context shortly after the first failed attempt
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := Do(ctx, cfg, func(ctx context.Context) error {
		atomic.AddInt32(&calls, 1)
		return errTemporary
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.LessOrEqual(t, atomic.LoadInt32(&calls), int32(2),
		"should not have retried many times before context was cancelled")
	assert.Less(t, elapsed, 500*time.Millisecond,
		"should have stopped quickly after context cancellation")
}

func TestRetry_JitterRange(t *testing.T) {
	// Run many iterations to check that jitter stays within ±25% of base delay.
	cfg := Config{
		MaxRetries:   1,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		JitterFrac:   0.25,
	}

	const iterations = 50
	baseDelay := cfg.InitialDelay
	minAllowed := time.Duration(float64(baseDelay) * (1.0 - cfg.JitterFrac))
	maxAllowed := time.Duration(float64(baseDelay) * (1.0 + cfg.JitterFrac))

	var minObserved, maxObserved time.Duration
	minObserved = time.Hour // start high

	for i := 0; i < iterations; i++ {
		var timestamps [2]time.Time
		idx := 0
		_ = Do(context.Background(), cfg, func(ctx context.Context) error {
			timestamps[idx] = time.Now()
			idx++
			return errTemporary
		})

		actual := timestamps[1].Sub(timestamps[0])
		if actual < minObserved {
			minObserved = actual
		}
		if actual > maxObserved {
			maxObserved = actual
		}

		// Each individual delay should be within a generous tolerance of the jitter range
		// (allowing for scheduling overhead)
		tolerance := 15 * time.Millisecond
		assert.GreaterOrEqual(t, actual, minAllowed-tolerance,
			"iteration %d: delay %v below minimum allowed %v (with tolerance)", i, actual, minAllowed)
		assert.LessOrEqual(t, actual, maxAllowed+tolerance,
			"iteration %d: delay %v above maximum allowed %v (with tolerance)", i, actual, maxAllowed)
	}

	// Over many iterations, we should see some spread, confirming jitter is applied.
	spread := maxObserved - minObserved
	assert.Greater(t, spread.Milliseconds(), int64(5),
		"jitter spread should be > 5ms across %d iterations, got %v", iterations, spread)
}
