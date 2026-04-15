package benchmark

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func TestEval_FalsePositiveRate(t *testing.T) {
	c, _, provider := newEvalClient(t, 0.95)
	seedUnrelated(t, c, provider, UnrelatedPairs)

	ctx := context.Background()
	falsePositives := 0
	for _, pair := range UnrelatedPairs {
		resp, err := c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    pair.Query,
		})
		require.NoError(t, err)
		if resp.Hit {
			falsePositives++
			t.Errorf("false positive: stored=%q query=%q tier=%s sim=%.4f",
				pair.Stored, pair.Query, resp.Tier, resp.Similarity)
		}
	}

	rate := float64(falsePositives) / float64(len(UnrelatedPairs))
	t.Logf("False positive rate: %.1f%% (%d/%d)", rate*100, falsePositives, len(UnrelatedPairs))
	assert.Zero(t, falsePositives, "no unrelated pair should produce a cache hit")
}

func TestEval_FalsePositiveRate_NearThreshold(t *testing.T) {
	// Register pairs at similarity just below the threshold (0.94 vs 0.95).
	// Validates the threshold boundary precisely.
	const threshold float32 = 0.95
	const belowThreshold float32 = 0.94

	c, _, provider := newEvalClient(t, threshold)
	ctx := context.Background()

	for i, pair := range Paraphrases {
		provider.RegisterPair(pair.Original, pair.Paraphrase, belowThreshold, int64(2000+i))
		_, err := c.Store(ctx, reverb.StoreRequest{
			Namespace: "bench",
			Prompt:    pair.Original,
			Response:  fmt.Sprintf("response for: %s", pair.Original),
		})
		require.NoError(t, err)
	}

	hits := 0
	for _, pair := range Paraphrases {
		resp, err := c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    pair.Paraphrase,
		})
		require.NoError(t, err)
		if resp.Hit && resp.Tier == "semantic" {
			hits++
			t.Errorf("unexpected hit at sim=%.2f < threshold=%.2f: %q → %q",
				belowThreshold, threshold, pair.Paraphrase, pair.Original)
		}
	}

	t.Logf("Near-threshold hits: %d/%d (expected 0)", hits, len(Paraphrases))
	assert.Zero(t, hits, "similarity=%.2f < threshold=%.2f should yield zero hits",
		belowThreshold, threshold)
}
