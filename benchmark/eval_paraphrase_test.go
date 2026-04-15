package benchmark

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func TestEval_ParaphraseHitPrecision(t *testing.T) {
	c, _, provider := newEvalClient(t, 0.95)
	seedParaphrases(t, c, provider, Paraphrases, 0.97)

	ctx := context.Background()
	hits := 0
	for _, pair := range Paraphrases {
		resp, err := c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    pair.Paraphrase,
		})
		require.NoError(t, err)
		if resp.Hit && resp.Tier == "semantic" {
			hits++
		} else {
			t.Errorf("miss for paraphrase: original=%q paraphrase=%q", pair.Original, pair.Paraphrase)
		}
	}

	precision := float64(hits) / float64(len(Paraphrases))
	t.Logf("Paraphrase hit precision: %.1f%% (%d/%d)", precision*100, hits, len(Paraphrases))
	assert.Equal(t, len(Paraphrases), hits, "all paraphrase pairs should produce semantic hits")
}

func TestEval_ParaphraseHitPrecision_ByCategory(t *testing.T) {
	c, _, provider := newEvalClient(t, 0.95)
	seedParaphrases(t, c, provider, Paraphrases, 0.97)

	ctx := context.Background()

	// Group by category.
	type result struct{ hits, total int }
	categories := make(map[string]*result)
	for _, pair := range Paraphrases {
		if categories[pair.Category] == nil {
			categories[pair.Category] = &result{}
		}
		r := categories[pair.Category]
		r.total++

		resp, err := c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    pair.Paraphrase,
		})
		require.NoError(t, err)
		if resp.Hit && resp.Tier == "semantic" {
			r.hits++
		}
	}

	t.Log("Category breakdown:")
	for cat, r := range categories {
		pct := float64(r.hits) / float64(r.total) * 100
		t.Logf("  %-20s %d/%d (%.0f%%)", cat, r.hits, r.total, pct)
		t.Run(cat, func(t *testing.T) {
			assert.Equal(t, r.total, r.hits, "category %s should have 100%% hit rate", cat)
		})
	}
}

func TestEval_ParaphraseThresholdSweep(t *testing.T) {
	thresholds := []float32{0.90, 0.92, 0.95, 0.97, 0.99}
	const pairSimilarity float32 = 0.97

	t.Logf("Pairs registered at similarity=%.2f", pairSimilarity)
	t.Log("Threshold | Hits | Total | Rate")
	t.Log("----------|------|-------|-----")

	for _, threshold := range thresholds {
		t.Run(fmt.Sprintf("threshold=%.2f", threshold), func(t *testing.T) {
			c, _, provider := newEvalClient(t, threshold)
			seedParaphrases(t, c, provider, Paraphrases, pairSimilarity)

			ctx := context.Background()
			hits := 0
			for _, pair := range Paraphrases {
				resp, err := c.Lookup(ctx, reverb.LookupRequest{
					Namespace: "bench",
					Prompt:    pair.Paraphrase,
				})
				require.NoError(t, err)
				if resp.Hit {
					hits++
				}
			}

			rate := float64(hits) / float64(len(Paraphrases))
			t.Logf("    %.2f  |  %2d  |  %2d   | %.0f%%", threshold, hits, len(Paraphrases), rate*100)

			// At thresholds <= similarity, we expect 100% hits.
			// At thresholds > similarity, we expect 0% hits.
			if threshold <= pairSimilarity {
				assert.Equal(t, len(Paraphrases), hits,
					"threshold=%.2f <= similarity=%.2f should yield 100%% hits", threshold, pairSimilarity)
			} else {
				assert.Zero(t, hits,
					"threshold=%.2f > similarity=%.2f should yield 0%% hits", threshold, pairSimilarity)
			}
		})
	}
}
