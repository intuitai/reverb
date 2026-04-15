package benchmark

import (
	"math"
	"math/rand"
)

// CraftSimilarPair returns two L2-normalized vectors whose cosine similarity
// equals targetSim (within float32 precision, typically ±0.001).
//
// The math: given unit vector v1 and a unit vector orth perpendicular to v1,
// v2 = s*v1 + sqrt(1-s²)*orth is a unit vector with cos(v1, v2) = s.
func CraftSimilarPair(dims int, targetSim float32, seed int64) ([]float32, []float32) {
	rng := rand.New(rand.NewSource(seed))

	// Generate random base vector and normalize.
	v1 := make([]float32, dims)
	for i := range v1 {
		v1[i] = float32(rng.NormFloat64())
	}
	L2Normalize(v1)

	// Build an orthogonal vector via Gram-Schmidt.
	orth := craftOrthogonal(v1, rng)

	// Construct v2 with the target similarity.
	s := float64(targetSim)
	perpScale := math.Sqrt(1 - s*s)
	v2 := make([]float32, dims)
	for i := range v2 {
		v2[i] = float32(s)*v1[i] + float32(perpScale)*orth[i]
	}
	L2Normalize(v2)

	return v1, v2
}

// craftOrthogonal returns a unit vector orthogonal to base using Gram-Schmidt.
func craftOrthogonal(base []float32, rng *rand.Rand) []float32 {
	dims := len(base)
	r := make([]float32, dims)
	for i := range r {
		r[i] = float32(rng.NormFloat64())
	}

	// Subtract projection onto base: r = r - (r·base)*base
	var dot float64
	for i := range r {
		dot += float64(r[i]) * float64(base[i])
	}
	for i := range r {
		r[i] -= float32(dot) * base[i]
	}

	L2Normalize(r)
	return r
}

// L2Normalize normalizes a vector to unit length in place and returns it.
func L2Normalize(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range v {
			v[i] = float32(float64(v[i]) / norm)
		}
	}
	return v
}

// CosineSim computes cosine similarity between two vectors.
func CosineSim(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
