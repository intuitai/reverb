package semantic

import (
	"context"
	"time"

	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/vector"
)

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Config holds semantic cache configuration.
type Config struct {
	Threshold    float32 // minimum cosine similarity for a hit
	TopK         int     // max candidates to retrieve
	ScopeByModel bool    // whether to filter results by model ID
}

// Cache implements the semantic (Tier 2) cache.
type Cache struct {
	embedder embedding.Provider
	index    vector.Index
	store    store.Store
	cfg      Config
	clock    Clock
}

// New creates a new semantic cache.
func New(embedder embedding.Provider, idx vector.Index, s store.Store, cfg Config, clock Clock) *Cache {
	if clock == nil {
		clock = realClock{}
	}
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.95
	}
	if cfg.TopK == 0 {
		cfg.TopK = 5
	}
	return &Cache{
		embedder: embedder,
		index:    idx,
		store:    s,
		cfg:      cfg,
		clock:    clock,
	}
}

// LookupResult holds the result of a semantic cache lookup.
type LookupResult struct {
	Hit        bool
	Entry      *store.CacheEntry
	Similarity float32
}

// Lookup searches the vector index for semantically similar prompts.
func (c *Cache) Lookup(ctx context.Context, namespace, normalizedPrompt, modelID string) (*LookupResult, error) {
	// Compute embedding
	emb, err := c.embedder.Embed(ctx, normalizedPrompt)
	if err != nil {
		// Graceful degradation: embedding failure → miss, no error
		return &LookupResult{Hit: false}, nil
	}

	// Search the vector index
	results, err := c.index.Search(ctx, emb, c.cfg.TopK, c.cfg.Threshold)
	if err != nil {
		return nil, err
	}

	now := c.clock.Now()

	// Check each candidate
	for _, res := range results {
		entry, err := c.store.Get(ctx, res.ID)
		if err != nil {
			return nil, err
		}
		if entry == nil {
			continue
		}
		// Verify namespace
		if entry.Namespace != namespace {
			continue
		}
		// Verify model ID if scoping is enabled
		if c.cfg.ScopeByModel && modelID != "" && entry.ModelID != modelID {
			continue
		}
		// Verify not expired
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			continue
		}
		return &LookupResult{
			Hit:        true,
			Entry:      entry,
			Similarity: res.Score,
		}, nil
	}

	return &LookupResult{Hit: false}, nil
}
