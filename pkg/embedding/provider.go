package embedding

import "context"

// Provider generates embedding vectors from text.
type Provider interface {
	// Embed returns the embedding vector for a single text input.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch returns embedding vectors for multiple text inputs.
	// Implementations should batch the API call where possible.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the dimensionality of the embedding vectors.
	Dimensions() int
}
