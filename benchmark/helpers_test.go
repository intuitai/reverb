package benchmark

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

const benchDims = 128

// newEvalClient creates a Reverb client backed by memory store + flat index
// with a ControlledProvider. ScopeByModel is false to simplify benchmarks.
func newEvalClient(t testing.TB, threshold float32) (*reverb.Client, *memory.Store, *ControlledProvider) {
	t.Helper()
	provider := NewControlledProvider(benchDims)
	s := memory.New()
	vi := flat.New(benchDims)
	clock := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultNamespace:    "bench",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: threshold,
		SemanticTopK:        5,
		ScopeByModel:        false,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, provider, s, vi)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c, s, provider
}

// seedParaphrases registers controlled vectors for each pair at the given
// similarity and stores the Original prompt in the cache.
func seedParaphrases(t testing.TB, c *reverb.Client, p *ControlledProvider, pairs []ParaphrasePair, similarity float32) {
	t.Helper()
	ctx := context.Background()
	for i, pair := range pairs {
		p.RegisterPair(pair.Original, pair.Paraphrase, similarity, int64(i))
		_, err := c.Store(ctx, reverb.StoreRequest{
			Namespace: "bench",
			Prompt:    pair.Original,
			Response:  fmt.Sprintf("response for: %s", pair.Original),
		})
		require.NoError(t, err)
	}
}

// seedUnrelated registers controlled vectors at low similarity and stores the
// Stored prompt in the cache.
func seedUnrelated(t testing.TB, c *reverb.Client, p *ControlledProvider, pairs []UnrelatedPair) {
	t.Helper()
	ctx := context.Background()
	for i, pair := range pairs {
		p.RegisterPair(pair.Stored, pair.Query, 0.3, int64(1000+i))
		_, err := c.Store(ctx, reverb.StoreRequest{
			Namespace: "bench",
			Prompt:    pair.Stored,
			Response:  fmt.Sprintf("response for: %s", pair.Stored),
		})
		require.NoError(t, err)
	}
}

// seedNEntries stores n entries with generated prompts. Returns the prompt
// used for entry 0 (for exact-hit benchmarks).
func seedNEntries(t testing.TB, c *reverb.Client, _ *ControlledProvider, n int) string {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		prompt := fmt.Sprintf("benchmark prompt number %d for load testing", i)
		hash := sha256.Sum256(fmt.Appendf(nil, "source-%d", i))
		_, err := c.Store(ctx, reverb.StoreRequest{
			Namespace: "bench",
			Prompt:    prompt,
			Response:  fmt.Sprintf("response %d", i),
			Sources: []store.SourceRef{
				{SourceID: fmt.Sprintf("doc:%d", i), ContentHash: hash},
			},
		})
		require.NoError(t, err)
	}
	return "benchmark prompt number 0 for load testing"
}
