package benchmark

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
)

func BenchmarkLookup_ExactHit(b *testing.B) {
	c, _, provider := newEvalClient(b, 0.95)
	prompt := seedNEntries(b, c, provider, 100)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    prompt,
		})
	}
}

func BenchmarkLookup_SemanticHit(b *testing.B) {
	c, _, provider := newEvalClient(b, 0.95)
	seedNEntries(b, c, provider, 100)

	// Register a paraphrase for entry 0.
	provider.RegisterPair(
		"benchmark prompt number 0 for load testing",
		"load testing benchmark prompt zero",
		0.97, 9999,
	)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    "load testing benchmark prompt zero",
		})
	}
}

func BenchmarkLookup_Miss(b *testing.B) {
	c, _, provider := newEvalClient(b, 0.95)
	seedNEntries(b, c, provider, 100)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		c.Lookup(ctx, reverb.LookupRequest{
			Namespace: "bench",
			Prompt:    "completely unrelated query that will miss the cache",
		})
	}
}

func BenchmarkStore(b *testing.B) {
	c, _, _ := newEvalClient(b, 0.95)
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		c.Store(ctx, reverb.StoreRequest{
			Namespace: "bench",
			Prompt:    fmt.Sprintf("store benchmark prompt %d", i),
			Response:  fmt.Sprintf("response %d", i),
			Sources: []store.SourceRef{
				{SourceID: fmt.Sprintf("doc:bench-%d", i), ContentHash: sha256.Sum256(fmt.Appendf(nil, "src-%d", i))},
			},
		})
	}
}

func BenchmarkLookup_ExactHit_ScaledIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			c, _, provider := newEvalClient(b, 0.95)
			prompt := seedNEntries(b, c, provider, n)
			ctx := context.Background()

			b.ResetTimer()
			for b.Loop() {
				c.Lookup(ctx, reverb.LookupRequest{
					Namespace: "bench",
					Prompt:    prompt,
				})
			}
		})
	}
}

func BenchmarkLookup_SemanticHit_ScaledIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			c, _, provider := newEvalClient(b, 0.95)
			seedNEntries(b, c, provider, n)
			provider.RegisterPair(
				"benchmark prompt number 0 for load testing",
				"load testing benchmark prompt zero",
				0.97, 9999,
			)
			ctx := context.Background()

			b.ResetTimer()
			for b.Loop() {
				c.Lookup(ctx, reverb.LookupRequest{
					Namespace: "bench",
					Prompt:    "load testing benchmark prompt zero",
				})
			}
		})
	}
}

func BenchmarkLookup_Miss_ScaledIndex(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			c, _, provider := newEvalClient(b, 0.95)
			seedNEntries(b, c, provider, n)
			ctx := context.Background()

			b.ResetTimer()
			for b.Loop() {
				c.Lookup(ctx, reverb.LookupRequest{
					Namespace: "bench",
					Prompt:    "completely unrelated query that will miss the cache",
				})
			}
		})
	}
}
