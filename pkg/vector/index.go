package vector

import "context"

// SearchResult represents a single result from a similarity search.
type SearchResult struct {
	ID    string
	Score float32 // cosine similarity, 0.0–1.0
}

// Index provides approximate nearest neighbor search over embedding vectors.
type Index interface {
	// Add inserts a vector with the given ID. If the ID already exists, it is overwritten.
	Add(ctx context.Context, id string, vector []float32) error

	// Search returns the top-k most similar vectors to the query, with similarity >= minScore.
	Search(ctx context.Context, query []float32, k int, minScore float32) ([]SearchResult, error)

	// Delete removes a vector by ID. No-op if not found.
	Delete(ctx context.Context, id string) error

	// Len returns the number of vectors in the index.
	Len() int
}
