package benchmark

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCraftSimilarPair_TargetSimilarity(t *testing.T) {
	targets := []float32{0.99, 0.97, 0.95, 0.90, 0.80, 0.50, 0.30}
	for _, target := range targets {
		v1, v2 := CraftSimilarPair(128, target, 42)
		actual := CosineSim(v1, v2)
		assert.InDelta(t, float64(target), float64(actual), 0.002,
			"target=%.2f got=%.4f", target, actual)
	}
}

func TestCraftSimilarPair_Deterministic(t *testing.T) {
	v1a, v2a := CraftSimilarPair(128, 0.95, 7)
	v1b, v2b := CraftSimilarPair(128, 0.95, 7)
	assert.Equal(t, v1a, v1b)
	assert.Equal(t, v2a, v2b)
}

func TestCraftSimilarPair_UnitLength(t *testing.T) {
	v1, v2 := CraftSimilarPair(128, 0.95, 1)
	assert.InDelta(t, 1.0, norm(v1), 0.001)
	assert.InDelta(t, 1.0, norm(v2), 0.001)
}

func TestControlledProvider_RegisterAndEmbed(t *testing.T) {
	p := NewControlledProvider(64)
	vec := make([]float32, 64)
	vec[0] = 1.0
	p.Register("test prompt", vec)

	ctx := context.Background()
	got, err := p.Embed(ctx, "test prompt")
	require.NoError(t, err)
	assert.Equal(t, vec, got)
}

func TestControlledProvider_NormalizationBridge(t *testing.T) {
	// Register with raw text; embed with what the Reverb client would send
	// (already normalized). The provider normalizes internally so both resolve
	// to the same key.
	p := NewControlledProvider(64)
	vec := make([]float32, 64)
	vec[0] = 1.0
	p.Register("Hello World?", vec)

	ctx := context.Background()
	// "Hello World?" normalizes to "hello world"
	got, err := p.Embed(ctx, "hello world")
	require.NoError(t, err)
	assert.Equal(t, vec, got, "normalized key should match")
}

func TestControlledProvider_FallbackToDeterministic(t *testing.T) {
	p := NewControlledProvider(64)
	ctx := context.Background()

	v1, err := p.Embed(ctx, "unregistered prompt")
	require.NoError(t, err)
	assert.Len(t, v1, 64)

	// Same text → same vector (deterministic fallback).
	v2, err := p.Embed(ctx, "unregistered prompt")
	require.NoError(t, err)
	assert.Equal(t, v1, v2)
}

func TestControlledProvider_RegisterPair(t *testing.T) {
	p := NewControlledProvider(128)
	p.RegisterPair("How do I reset?", "Steps to reset?", 0.97, 0)

	ctx := context.Background()
	v1, err := p.Embed(ctx, "how do i reset")
	require.NoError(t, err)
	v2, err := p.Embed(ctx, "steps to reset")
	require.NoError(t, err)

	sim := CosineSim(v1, v2)
	assert.InDelta(t, 0.97, float64(sim), 0.002)
}

func TestControlledProvider_EmbedBatch(t *testing.T) {
	p := NewControlledProvider(64)
	vec := make([]float32, 64)
	vec[0] = 1.0
	p.Register("a", vec)

	ctx := context.Background()
	results, err := p.EmbedBatch(ctx, []string{"a", "unknown"})
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, vec, results[0])
	assert.Len(t, results[1], 64)
}

func TestControlledProvider_ReturnsCopy(t *testing.T) {
	p := NewControlledProvider(64)
	vec := make([]float32, 64)
	vec[0] = 1.0
	p.Register("test", vec)

	ctx := context.Background()
	got, _ := p.Embed(ctx, "test")
	got[0] = 999.0 // mutate the returned copy

	got2, _ := p.Embed(ctx, "test")
	assert.Equal(t, float32(1.0), got2[0], "mutation should not affect stored vector")
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}
