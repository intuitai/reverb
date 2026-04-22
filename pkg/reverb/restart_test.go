package reverb_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
	badgerstore "github.com/nobelk/reverb/pkg/store/badger"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// baseCfg returns a minimal Config suitable for the restart tests. Each test
// gets its own clock so time-based behavior stays deterministic.
func restartCfg(clock reverb.Clock) reverb.Config {
	return reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
		Clock:               clock,
	}
}

// TestRestart_VectorIndexIsColdWithoutRebuild documents the default behavior:
// the store persists across client restarts, but the vector index does not.
// After a restart, exact-tier lookups still hit; semantic-tier lookups miss
// until new Store calls repopulate the index.
func TestRestart_VectorIndexIsColdWithoutRebuild(t *testing.T) {
	ctx := context.Background()
	clock := testutil.NewFakeClock(time.Now())
	s := memory.New()
	vi1 := flat.New(0)
	embedder := fake.New(dims)

	// First lifetime: store an entry, confirm both tiers warm.
	c1, err := reverb.New(restartCfg(clock), embedder, s, vi1)
	require.NoError(t, err)

	_, err = c1.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "How do I reset my password?",
		ModelID:   "gpt-4",
		Response:  "Visit settings.",
	})
	require.NoError(t, err)
	require.Equal(t, 1, vi1.Len(), "index should have the entry after initial store")

	require.NoError(t, c1.Close())

	// Second lifetime: same store (state preserved), brand-new vector index.
	vi2 := flat.New(0)
	c2, err := reverb.New(restartCfg(clock), embedder, s, vi2)
	require.NoError(t, err)
	defer c2.Close()

	assert.Equal(t, 0, vi2.Len(), "vector index is cold on restart — this is the gap rebuild closes")

	// Exact lookup still hits: the prompt hash lives in the store.
	exactResp, err := c2.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns", Prompt: "How do I reset my password?", ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, exactResp.Hit, "exact hit should survive restart — it reads from the store")
	assert.Equal(t, "exact", exactResp.Tier)

	// Semantic lookup misses: slightly different prompt normalizes to a different
	// hash, the vector index is empty, and no semantic match is possible.
	semResp, err := c2.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns", Prompt: "how to reset my password", ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, semResp.Hit, "semantic tier is silently empty until warmup — documented restart gap")
}

// TestRestart_RebuildVectorIndexRestoresSemantic verifies that enabling
// WithRebuildVectorIndex on the second lifetime re-populates the index from
// the store so semantic lookups hit immediately.
func TestRestart_RebuildVectorIndexRestoresSemantic(t *testing.T) {
	ctx := context.Background()
	clock := testutil.NewFakeClock(time.Now())
	s := memory.New()
	embedder := fake.New(dims)

	c1, err := reverb.New(restartCfg(clock), embedder, s, flat.New(0))
	require.NoError(t, err)

	// Seed two entries across two namespaces so per-namespace iteration is exercised.
	seeded := []struct {
		namespace string
		prompt    string
	}{
		{"ns-a", "How do I reset my password?"},
		{"ns-a", "Where is the settings menu?"},
		{"ns-b", "What time does support open?"},
	}
	for _, e := range seeded {
		_, err := c1.Store(ctx, reverb.StoreRequest{
			Namespace: e.namespace, Prompt: e.prompt, ModelID: "gpt-4", Response: "ok",
		})
		require.NoError(t, err)
	}
	require.NoError(t, c1.Close())

	// Second lifetime with rebuild enabled.
	vi2 := flat.New(0)
	c2, err := reverb.New(restartCfg(clock), embedder, s, vi2, reverb.WithRebuildVectorIndex(true))
	require.NoError(t, err)
	defer c2.Close()

	assert.Equal(t, len(seeded), vi2.Len(), "rebuild should re-add every seeded entry to the index")

	// Semantic tier works immediately — the rebuilt index contains the original
	// vectors and the fake embedder is deterministic, so identical prompts hit.
	for _, e := range seeded {
		resp, err := c2.Lookup(ctx, reverb.LookupRequest{
			Namespace: e.namespace, Prompt: e.prompt, ModelID: "gpt-4",
		})
		require.NoError(t, err)
		assert.True(t, resp.Hit, "entry %q in %q should hit after rebuild", e.prompt, e.namespace)
	}
}

// TestRestart_RebuildSkipsExpiredAndEmbeddingMissing verifies the rebuild
// scan skips entries with no usable embedding. Without this filter the index
// would briefly contain garbage the reaper is about to delete, or attempt to
// call Add with an empty vector.
func TestRestart_RebuildSkipsExpiredAndEmbeddingMissing(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	clock := testutil.NewFakeClock(now)
	s := memory.New()
	embedder := fake.New(dims)
	validVec, err := embedder.Embed(ctx, "valid")
	require.NoError(t, err)
	expiredVec, err := embedder.Embed(ctx, "expired")
	require.NoError(t, err)

	// Seed directly into the store so we can craft the skip cases precisely.
	// All three entries share a namespace to keep the Stats().Namespaces path simple.
	valid := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt("valid").
		WithEmbedding(validVec).
		WithTTL(time.Hour).
		Build()
	expired := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt("expired").
		WithEmbedding(expiredVec).
		WithExpiresAt(now.Add(-1 * time.Hour)).
		Build()
	noEmbedding := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt("no-embedding").
		WithTTL(time.Hour).
		Build()
	noEmbedding.EmbeddingMissing = true

	for _, e := range []*store.CacheEntry{valid, expired, noEmbedding} {
		require.NoError(t, s.Put(ctx, e))
	}

	vi := flat.New(0)
	var idx vector.Index = vi

	c, err := reverb.New(restartCfg(clock), embedder, s, idx, reverb.WithRebuildVectorIndex(true))
	require.NoError(t, err)
	defer c.Close()

	assert.Equal(t, 1, vi.Len(), "rebuild should add only the valid entry; expired and embedding-missing are skipped")
}

// TestRestart_BadgerRestartThroughClient exercises the full restart path:
// client lifetime 1 writes to a real Badger directory, lifetime 2 reopens
// the directory and rebuilds the vector index. Semantic hits must survive.
func TestRestart_BadgerRestartThroughClient(t *testing.T) {
	ctx := context.Background()
	clock := testutil.NewFakeClock(time.Now())
	embedder := fake.New(dims)
	dir := filepath.Join(t.TempDir(), "badger")

	store1, err := badgerstore.New(dir)
	require.NoError(t, err)

	c1, err := reverb.New(restartCfg(clock), embedder, store1, flat.New(0))
	require.NoError(t, err)
	_, err = c1.Store(ctx, reverb.StoreRequest{
		Namespace: "ns", Prompt: "persistent prompt", ModelID: "gpt-4", Response: "persisted",
	})
	require.NoError(t, err)
	// c1.Close() also closes the Badger handle.
	require.NoError(t, c1.Close())

	// Reopen the same directory and rebuild.
	store2, err := badgerstore.New(dir)
	require.NoError(t, err)
	vi2 := flat.New(0)
	c2, err := reverb.New(restartCfg(clock), embedder, store2, vi2, reverb.WithRebuildVectorIndex(true))
	require.NoError(t, err)
	defer c2.Close()

	assert.Equal(t, 1, vi2.Len(), "rebuild should restore the single persisted entry")

	resp, err := c2.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns", Prompt: "persistent prompt", ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit, "entry should be reachable after Badger restart + index rebuild")
}
