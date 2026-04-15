package benchmark

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/lineage"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func TestEval_InvalidationRemovesEntries(t *testing.T) {
	c, _, provider := newEvalClient(t, 0.95)
	ctx := context.Background()

	sourceHash := sha256.Sum256([]byte("original content"))
	provider.RegisterPair("invalidation test prompt", "rephrase of invalidation test", 0.97, 500)

	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "bench",
		Prompt:    "invalidation test prompt",
		Response:  "cached response",
		Sources: []store.SourceRef{
			{SourceID: "doc:inv-test", ContentHash: sourceHash},
		},
	})
	require.NoError(t, err)

	// Verify hit before invalidation.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "invalidation test prompt"})
	require.NoError(t, err)
	require.True(t, resp.Hit, "should hit before invalidation")

	// Invalidate the source.
	count, err := c.Invalidate(ctx, "doc:inv-test")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Exact lookup should miss.
	resp, err = c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "invalidation test prompt"})
	require.NoError(t, err)
	assert.False(t, resp.Hit, "exact lookup should miss after invalidation")

	// Semantic lookup should also miss.
	resp, err = c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "rephrase of invalidation test"})
	require.NoError(t, err)
	assert.False(t, resp.Hit, "semantic lookup should miss after invalidation")
}

func TestEval_InvalidationContentHashChange(t *testing.T) {
	// Use the invalidator directly to test content-hash-change semantics
	// (the client.Invalidate API always sends zero hash = deletion).
	s := memory.New()
	vi := flat.New(benchDims)
	provider := NewControlledProvider(benchDims)
	clock := testutil.NewFakeClock(time.Now())

	cfg := reverb.Config{
		DefaultNamespace:    "bench",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        false,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, provider, s, vi)
	require.NoError(t, err)
	defer c.Close()

	ctx := context.Background()
	hashV1 := sha256.Sum256([]byte("version 1"))

	_, err = c.Store(ctx, reverb.StoreRequest{
		Namespace: "bench",
		Prompt:    "content hash test",
		Response:  "response v1",
		Sources:   []store.SourceRef{{SourceID: "doc:versioned", ContentHash: hashV1}},
	})
	require.NoError(t, err)

	// Construct invalidator directly to send a content-hash-change event.
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)

	hashV2 := sha256.Sum256([]byte("version 2"))
	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "doc:versioned",
		ContentHash: hashV2,
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, count, "hash change should invalidate the entry")

	// Verify it's gone.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "content hash test"})
	require.NoError(t, err)
	assert.False(t, resp.Hit, "entry should be invalidated after content hash change")
}

func TestEval_InvalidationNoFalseInvalidation(t *testing.T) {
	c, _, _ := newEvalClient(t, 0.95)
	ctx := context.Background()

	hashA := sha256.Sum256([]byte("source A"))
	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "bench",
		Prompt:    "entry linked to source A",
		Response:  "response A",
		Sources:   []store.SourceRef{{SourceID: "doc:A", ContentHash: hashA}},
	})
	require.NoError(t, err)

	// Invalidate unrelated source.
	count, err := c.Invalidate(ctx, "doc:B")
	require.NoError(t, err)
	assert.Equal(t, 0, count, "invalidating unrelated source should not remove entries")

	// Entry should still be there.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "entry linked to source A"})
	require.NoError(t, err)
	assert.True(t, resp.Hit, "entry with source A should survive invalidation of source B")
}

func TestEval_InvalidationMultipleSources(t *testing.T) {
	c, _, _ := newEvalClient(t, 0.95)
	ctx := context.Background()

	hashA := sha256.Sum256([]byte("source A"))
	hashB := sha256.Sum256([]byte("source B"))
	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "bench",
		Prompt:    "entry with two sources",
		Response:  "multi-source response",
		Sources: []store.SourceRef{
			{SourceID: "doc:A", ContentHash: hashA},
			{SourceID: "doc:B", ContentHash: hashB},
		},
	})
	require.NoError(t, err)

	// Invalidate just one of the two sources.
	count, err := c.Invalidate(ctx, "doc:A")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "invalidating one source should remove the entry")

	resp, err := c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "entry with two sources"})
	require.NoError(t, err)
	assert.False(t, resp.Hit, "entry should be gone after invalidating one of its sources")
}

func TestEval_InvalidationIdempotent(t *testing.T) {
	// Sending the same content hash should NOT invalidate.
	s := memory.New()
	vi := flat.New(benchDims)
	provider := NewControlledProvider(benchDims)
	clock := testutil.NewFakeClock(time.Now())

	cfg := reverb.Config{
		DefaultNamespace:    "bench",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        false,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, provider, s, vi)
	require.NoError(t, err)
	defer c.Close()

	ctx := context.Background()
	contentHash := sha256.Sum256([]byte("unchanged content"))

	_, err = c.Store(ctx, reverb.StoreRequest{
		Namespace: "bench",
		Prompt:    "idempotent test",
		Response:  "should survive",
		Sources:   []store.SourceRef{{SourceID: "doc:stable", ContentHash: contentHash}},
	})
	require.NoError(t, err)

	// Send a change event with the SAME hash.
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)

	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "doc:stable",
		ContentHash: contentHash, // same hash → no change
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, count, "same content hash should not invalidate")

	// Entry should survive.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{Namespace: "bench", Prompt: "idempotent test"})
	require.NoError(t, err)
	assert.True(t, resp.Hit, "entry should survive when content hash unchanged")
}
