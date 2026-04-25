package throttled_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/embedding/throttled"
	"github.com/nobelk/reverb/pkg/limiter"
)

// blockingProvider blocks every Embed call on a release channel so tests
// can deterministically saturate the concurrency limiter.
type blockingProvider struct {
	dims    int
	release chan struct{}
	calls   int
	mu      sync.Mutex
}

func (b *blockingProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	select {
	case <-b.release:
		return make([]float32, b.dims), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, b.dims)
	}
	return out, nil
}

func (b *blockingProvider) Dimensions() int { return b.dims }

func TestThrottled_NilLimiterIsPassthrough(t *testing.T) {
	inner := fake.New(8)
	wrapped := throttled.New(inner, nil, nil)
	// Should be the same value since wrapping is skipped.
	assert.Equal(t, embedding.Provider(inner), wrapped)
}

func TestThrottled_RejectsBeyondInFlightAndQueue(t *testing.T) {
	bp := &blockingProvider{dims: 4, release: make(chan struct{})}
	cl := limiter.NewConcurrencyLimiter(1, 1, 50*time.Millisecond)
	p := throttled.New(bp, cl, nil)

	// Hold the single in-flight slot.
	first := make(chan error, 1)
	go func() {
		_, err := p.Embed(context.Background(), "a")
		first <- err
	}()

	// Wait until the first call is actually executing inside the inner provider.
	require.Eventually(t, func() bool {
		bp.mu.Lock()
		defer bp.mu.Unlock()
		return bp.calls >= 1
	}, time.Second, 5*time.Millisecond)

	// Second call queues (queue size 1) and should time out → ErrOverloaded.
	second := make(chan error, 1)
	go func() {
		_, err := p.Embed(context.Background(), "b")
		second <- err
	}()

	// Third call has nowhere to go — must be rejected immediately.
	_, err := p.Embed(context.Background(), "c")
	assert.ErrorIs(t, err, limiter.ErrOverloaded)

	// Queued one eventually times out without ever reaching the inner provider.
	select {
	case err := <-second:
		assert.ErrorIs(t, err, limiter.ErrOverloaded)
	case <-time.After(time.Second):
		t.Fatal("queued call should have timed out")
	}

	// Release the original holder so it completes cleanly.
	close(bp.release)
	require.NoError(t, <-first)
}

func TestThrottled_DimensionsPassthrough(t *testing.T) {
	inner := fake.New(123)
	cl := limiter.NewConcurrencyLimiter(1, 0, 0)
	p := throttled.New(inner, cl, nil)
	assert.Equal(t, 123, p.Dimensions())
}
