package polling

import (
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"
	"time"

	"github.com/org/reverb/pkg/cdc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolling_DetectsChange(t *testing.T) {
	var callCount atomic.Int32
	hashA := sha256.Sum256([]byte("version-1"))
	hashB := sha256.Sum256([]byte("version-2"))

	hashFn := func(_ context.Context, sourceID string) ([32]byte, error) {
		n := callCount.Add(1)
		// First call returns hashA (seed), second call returns hashB (change).
		if n <= 1 {
			return hashA, nil
		}
		return hashB, nil
	}

	l := New(Config{
		Interval: 20 * time.Millisecond,
		Sources:  []string{"doc:test"},
		HashFn:   hashFn,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan cdc.ChangeEvent, 10)

	go func() {
		_ = l.Start(ctx, events)
	}()

	select {
	case event := <-events:
		assert.Equal(t, "doc:test", event.SourceID)
		assert.Equal(t, hashB, event.ContentHash)
		assert.NotEmpty(t, event.ContentHashHex)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for change event")
	}

	cancel()
}

func TestPolling_NoChange(t *testing.T) {
	stableHash := sha256.Sum256([]byte("stable-content"))

	hashFn := func(_ context.Context, sourceID string) ([32]byte, error) {
		return stableHash, nil
	}

	l := New(Config{
		Interval: 20 * time.Millisecond,
		Sources:  []string{"doc:stable"},
		HashFn:   hashFn,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	events := make(chan cdc.ChangeEvent, 10)

	go func() {
		_ = l.Start(ctx, events)
	}()

	// Wait for the context to expire, then check that no events were emitted.
	<-ctx.Done()

	// Small grace period for any in-flight goroutine work.
	time.Sleep(20 * time.Millisecond)

	select {
	case event := <-events:
		t.Fatalf("expected no events, got: %+v", event)
	default:
		// No event received -- correct behavior.
	}
}

func TestPolling_Shutdown(t *testing.T) {
	stableHash := sha256.Sum256([]byte("content"))

	hashFn := func(_ context.Context, sourceID string) ([32]byte, error) {
		return stableHash, nil
	}

	l := New(Config{
		Interval: 20 * time.Millisecond,
		Sources:  []string{"doc:shutdown"},
		HashFn:   hashFn,
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan cdc.ChangeEvent, 10)

	errCh := make(chan error, 1)
	go func() {
		errCh <- l.Start(ctx, events)
	}()

	// Let a few poll cycles run.
	time.Sleep(80 * time.Millisecond)

	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poller to stop")
	}
}
