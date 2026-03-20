package testutil

import (
	"hash/fnv"
	"math"
)

// DeterministicEmbedding generates a deterministic, L2-normalized vector from text.
// Used in tests to generate predictable embeddings.
func DeterministicEmbedding(text string, dims int) []float32 {
	vec := make([]float32, dims)
	for i := range vec {
		h := fnv.New64a()
		h.Write([]byte{byte(i), byte(i >> 8)})
		h.Write([]byte(text))
		bits := h.Sum64()
		vec[i] = float32(bits&0xFFFF) / 0xFFFF
	}
	// L2 normalize
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}
