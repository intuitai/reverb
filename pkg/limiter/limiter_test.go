package limiter_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/limiter"
)

func TestTokenBucket_StartsFullAndDrainsOnAllow(t *testing.T) {
	clk := testutil.NewFakeClock(time.Unix(0, 0))
	tb := limiter.NewTokenBucket(10, 3, clk)

	for i := range 3 {
		assert.True(t, tb.Allow(), "burst slot %d should be allowed", i)
	}
	assert.False(t, tb.Allow(), "fourth call should be rejected with no time elapsed")
}

func TestTokenBucket_RefillsOverTime(t *testing.T) {
	clk := testutil.NewFakeClock(time.Unix(0, 0))
	tb := limiter.NewTokenBucket(2, 1, clk) // 2 tok/s, burst of 1

	require.True(t, tb.Allow())
	require.False(t, tb.Allow())

	// Half a second yields exactly one token at 2/s.
	clk.Advance(500 * time.Millisecond)
	assert.True(t, tb.Allow(), "token should have refilled after 500ms at 2/s")
	assert.False(t, tb.Allow())
}

func TestTokenBucket_RetryAfterReportsTimeUntilNextToken(t *testing.T) {
	clk := testutil.NewFakeClock(time.Unix(0, 0))
	tb := limiter.NewTokenBucket(1, 1, clk) // 1 tok/s

	require.True(t, tb.Allow())
	wait := tb.RetryAfter()
	// One full second to refill from 0 to 1 token.
	assert.InDelta(t, time.Second, wait, float64(50*time.Millisecond))
}

func TestTokenBucket_DisabledWhenRateZero(t *testing.T) {
	tb := limiter.NewTokenBucket(0, 1, nil)
	require.True(t, tb.Allow(), "first burst token should be allowed")
	assert.False(t, tb.Allow())
	// With rate=0, RetryAfter falls back to a sensible cap.
	assert.Equal(t, time.Minute, tb.RetryAfter())
}

func TestRegistry_PerTenantIsolation(t *testing.T) {
	clk := testutil.NewFakeClock(time.Unix(0, 0))
	reg := limiter.NewRegistry(10, 1, clk)
	require.NotNil(t, reg)

	okA, _ := reg.Allow("tenant-a")
	require.True(t, okA)
	denyA, _ := reg.Allow("tenant-a")
	require.False(t, denyA, "tenant-a should be rate limited after one request")

	// tenant-b has its own bucket and is unaffected.
	okB, _ := reg.Allow("tenant-b")
	assert.True(t, okB)
}

func TestRegistry_NilWhenDisabled(t *testing.T) {
	assert.Nil(t, limiter.NewRegistry(0, 5, nil))
	assert.Nil(t, limiter.NewRegistry(5, 0, nil))
}

func TestRegistry_EmptyTenantUsesAnonymous(t *testing.T) {
	clk := testutil.NewFakeClock(time.Unix(0, 0))
	reg := limiter.NewRegistry(10, 1, clk)

	ok1, _ := reg.Allow("")
	require.True(t, ok1)
	// Same anonymous bucket — second call denied.
	ok2, _ := reg.Allow(limiter.AnonymousTenant)
	assert.False(t, ok2)
}

func TestConcurrencyLimiter_RespectsInFlightCap(t *testing.T) {
	cl := limiter.NewConcurrencyLimiter(2, 0, 0)
	require.NotNil(t, cl)

	require.NoError(t, cl.Acquire(context.Background()))
	require.NoError(t, cl.Acquire(context.Background()))
	// No queue, no wait — third must overload immediately.
	err := cl.Acquire(context.Background())
	assert.ErrorIs(t, err, limiter.ErrOverloaded)

	cl.Release()
	assert.NoError(t, cl.Acquire(context.Background()))
}

func TestConcurrencyLimiter_BoundedQueueRejectsExcessWaiters(t *testing.T) {
	cl := limiter.NewConcurrencyLimiter(1, 1, 100*time.Millisecond)
	require.NotNil(t, cl)

	// Fill the in-flight slot.
	require.NoError(t, cl.Acquire(context.Background()))

	// One waiter is allowed (queue size 1) but will time out.
	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- cl.Acquire(context.Background())
	}()

	// Give the waiter time to register itself in the queue.
	require.Eventually(t, func() bool {
		return cl.QueueDepth() == 1
	}, time.Second, 5*time.Millisecond)

	// A second waiter must be rejected immediately (queue is full).
	err := cl.Acquire(context.Background())
	assert.ErrorIs(t, err, limiter.ErrOverloaded)

	// The first waiter eventually times out.
	select {
	case got := <-waiterDone:
		assert.ErrorIs(t, got, limiter.ErrOverloaded)
	case <-time.After(time.Second):
		t.Fatal("queued waiter should have timed out")
	}
}

func TestConcurrencyLimiter_QueuedAcquireSucceedsOnRelease(t *testing.T) {
	cl := limiter.NewConcurrencyLimiter(1, 1, time.Second)
	require.NotNil(t, cl)

	require.NoError(t, cl.Acquire(context.Background()))

	got := make(chan error, 1)
	go func() {
		got <- cl.Acquire(context.Background())
	}()

	// Wait for the goroutine to enter the queue.
	require.Eventually(t, func() bool {
		return cl.QueueDepth() == 1
	}, time.Second, 5*time.Millisecond)

	cl.Release()
	select {
	case err := <-got:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("queued acquire should have succeeded after release")
	}
}

func TestConcurrencyLimiter_RespectsContextCancellation(t *testing.T) {
	cl := limiter.NewConcurrencyLimiter(1, 1, time.Minute)
	require.NoError(t, cl.Acquire(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		got <- cl.Acquire(ctx)
	}()

	require.Eventually(t, func() bool {
		return cl.QueueDepth() == 1
	}, time.Second, 5*time.Millisecond)

	cancel()
	select {
	case err := <-got:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("queued acquire should have observed cancellation")
	}
}

func TestConcurrencyLimiter_NilWhenDisabled(t *testing.T) {
	assert.Nil(t, limiter.NewConcurrencyLimiter(0, 5, time.Second))
}

// TestConcurrencyLimiter_NoLeakUnderLoad spins up many goroutines competing
// for a tiny pool to make sure waiter accounting (queue depth) returns to
// zero once everyone has been served or rejected.
func TestConcurrencyLimiter_NoLeakUnderLoad(t *testing.T) {
	cl := limiter.NewConcurrencyLimiter(2, 8, 200*time.Millisecond)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if err := cl.Acquire(context.Background()); err == nil {
				time.Sleep(5 * time.Millisecond)
				cl.Release()
			}
		})
	}
	wg.Wait()

	assert.Equal(t, int64(0), cl.QueueDepth(), "all waiters should have drained")
	assert.Equal(t, 0, cl.InFlight(), "no slots should remain held")
}
