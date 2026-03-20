package hnsw_test

import (
	"testing"

	"github.com/org/reverb/pkg/vector"
	"github.com/org/reverb/pkg/vector/conformance"
	"github.com/org/reverb/pkg/vector/hnsw"
)

func TestHNSWIndexConformance(t *testing.T) {
	conformance.RunVectorIndexConformance(t, func(t *testing.T, dims int) vector.Index {
		return hnsw.New(hnsw.Config{M: 16, EfConstruction: 200, EfSearch: 100}, dims)
	})
}
