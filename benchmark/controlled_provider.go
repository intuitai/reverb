package benchmark

import (
	"context"
	"sync"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/normalize"
)

// ControlledProvider is an embedding provider that returns pre-registered
// vectors for known prompts and falls back to deterministic FNV hashing for
// unknown prompts. This lets benchmarks craft exact cosine similarities
// between prompt pairs while keeping unregistered prompts deterministic.
type ControlledProvider struct {
	mu       sync.RWMutex
	dims     int
	mappings map[string][]float32 // normalized text → vector
	fallback *fake.Provider
}

// NewControlledProvider creates a provider with the given vector dimensionality.
func NewControlledProvider(dims int) *ControlledProvider {
	return &ControlledProvider{
		dims:     dims,
		mappings: make(map[string][]float32),
		fallback: fake.New(dims),
	}
}

// Register maps a prompt to a specific embedding vector. The prompt is
// normalized before storing because the Reverb client normalizes prompts
// before calling Embed.
func (p *ControlledProvider) Register(text string, vec []float32) {
	key := normalize.Normalize(text)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mappings[key] = vec
}

// RegisterPair registers two prompts with vectors that have the target cosine
// similarity. Each pair gets a unique seed for reproducible vector generation.
func (p *ControlledProvider) RegisterPair(textA, textB string, targetSim float32, seed int64) {
	v1, v2 := CraftSimilarPair(p.dims, targetSim, seed)
	p.Register(textA, v1)
	p.Register(textB, v2)
}

// Embed returns the registered vector for text if one exists, otherwise falls
// back to deterministic FNV hashing.
func (p *ControlledProvider) Embed(_ context.Context, text string) ([]float32, error) {
	key := normalize.Normalize(text)
	p.mu.RLock()
	if vec, ok := p.mappings[key]; ok {
		p.mu.RUnlock()
		// Return a copy to prevent mutation.
		out := make([]float32, len(vec))
		copy(out, vec)
		return out, nil
	}
	p.mu.RUnlock()
	return p.fallback.Embed(context.Background(), text)
}

// EmbedBatch returns embeddings for multiple texts.
func (p *ControlledProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := p.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		result[i] = vec
	}
	return result, nil
}

// Dimensions returns the vector dimensionality.
func (p *ControlledProvider) Dimensions() int { return p.dims }
