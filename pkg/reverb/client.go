package reverb

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/org/reverb/internal/hashutil"
	"github.com/org/reverb/pkg/cache/exact"
	"github.com/org/reverb/pkg/cache/semantic"
	"github.com/org/reverb/pkg/embedding"
	"github.com/org/reverb/pkg/lineage"
	"github.com/org/reverb/pkg/normalize"
	"github.com/org/reverb/pkg/store"
	"github.com/org/reverb/pkg/vector"
)

// SourceRef records the identity and content hash of a source document.
type SourceRef = store.SourceRef

// CacheEntry is the atomic unit stored by Reverb.
type CacheEntry = store.CacheEntry

// LookupRequest holds the parameters for a cache lookup.
type LookupRequest struct {
	Namespace string
	Prompt    string
	ModelID   string
}

// LookupResponse holds the result of a cache lookup.
type LookupResponse struct {
	Hit        bool
	Tier       string  // "exact" | "semantic" | ""
	Similarity float32 // 1.0 for exact, 0.0–1.0 for semantic
	Entry      *CacheEntry
}

// StoreRequest holds the parameters for storing a cache entry.
type StoreRequest struct {
	Namespace    string
	Prompt       string
	ModelID      string
	Response     string
	ResponseMeta map[string]string
	Sources      []SourceRef
	TTL          time.Duration
}

// Stats holds cache statistics.
type Stats struct {
	TotalEntries        int64
	Namespaces          []string
	ExactHitsTotal      int64
	SemanticHitsTotal   int64
	MissesTotal         int64
	InvalidationsTotal  int64
}

// Client is the primary entry point for Reverb.
// It is safe for concurrent use.
type Client struct {
	cfg          Config
	embedder     embedding.Provider
	exactTier    *exact.Cache
	semanticTier *semantic.Cache
	store        store.Store
	vectorIndex  vector.Index
	invalidator  *lineage.Invalidator
	lineageIdx   *lineage.Index
	clock        Clock
	logger       *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu                 sync.Mutex
	exactHits          int64
	semanticHits       int64
	misses             int64
	invalidationsTotal int64
}

// New creates a new Reverb client with the given configuration and pre-built dependencies.
func New(cfg Config, embedder embedding.Provider, s store.Store, vi vector.Index) (*Client, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	logger := slog.Default()

	lineageIdx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, lineageIdx, logger)

	exactCache := exact.New(s, cfg.Clock)
	semanticCache := semantic.New(embedder, vi, s, semantic.Config{
		Threshold:    cfg.SimilarityThreshold,
		TopK:         cfg.SemanticTopK,
		ScopeByModel: cfg.ScopeByModel,
	}, cfg.Clock)

	c := &Client{
		cfg:          cfg,
		embedder:     embedder,
		exactTier:    exactCache,
		semanticTier: semanticCache,
		store:        s,
		vectorIndex:  vi,
		invalidator:  inv,
		lineageIdx:   lineageIdx,
		clock:        cfg.Clock,
		logger:       logger,
	}

	return c, nil
}

// Lookup checks the cache for a matching response.
func (c *Client) Lookup(ctx context.Context, req LookupRequest) (*LookupResponse, error) {
	ns := req.Namespace
	if ns == "" {
		ns = c.cfg.DefaultNamespace
	}

	normalized := normalize.Normalize(req.Prompt)
	modelID := req.ModelID

	// Tier 1: Exact match
	hash := hashutil.PromptHash(ns, normalized, modelID)
	exactResult, err := c.exactTier.Lookup(ctx, ns, hash)
	if err != nil {
		return nil, err
	}
	if exactResult.Hit {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			_ = c.store.IncrementHit(context.Background(), exactResult.Entry.ID)
		}()
		c.mu.Lock()
		c.exactHits++
		c.mu.Unlock()
		c.logger.Info("cache hit",
			"tier", "exact",
			"namespace", ns,
			"entry_id", exactResult.Entry.ID)
		return &LookupResponse{
			Hit:        true,
			Tier:       "exact",
			Similarity: 1.0,
			Entry:      exactResult.Entry,
		}, nil
	}

	// Tier 2: Semantic match
	semanticResult, err := c.semanticTier.Lookup(ctx, ns, normalized, modelID)
	if err != nil {
		return nil, err
	}
	if semanticResult.Hit {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			_ = c.store.IncrementHit(context.Background(), semanticResult.Entry.ID)
		}()
		c.mu.Lock()
		c.semanticHits++
		c.mu.Unlock()
		c.logger.Info("cache hit",
			"tier", "semantic",
			"namespace", ns,
			"similarity", semanticResult.Similarity,
			"entry_id", semanticResult.Entry.ID)
		return &LookupResponse{
			Hit:        true,
			Tier:       "semantic",
			Similarity: semanticResult.Similarity,
			Entry:      semanticResult.Entry,
		}, nil
	}

	// Miss
	c.mu.Lock()
	c.misses++
	c.mu.Unlock()
	return &LookupResponse{Hit: false}, nil
}

// Store writes a new cache entry.
func (c *Client) Store(ctx context.Context, req StoreRequest) (*CacheEntry, error) {
	ns := req.Namespace
	if ns == "" {
		ns = c.cfg.DefaultNamespace
	}

	normalized := normalize.Normalize(req.Prompt)
	hash := hashutil.PromptHash(ns, normalized, req.ModelID)

	ttl := req.TTL
	if ttl == 0 {
		ttl = c.cfg.DefaultTTL
	}

	now := c.clock.Now()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	// Compute embedding
	var emb []float32
	var embeddingMissing bool
	embResult, err := c.embedder.Embed(ctx, normalized)
	if err != nil {
		c.logger.Warn("embedding failed during store, storing in exact tier only",
			"error", err)
		embeddingMissing = true
	} else {
		emb = embResult
	}

	entry := &CacheEntry{
		ID:               uuid.New().String(),
		CreatedAt:        now,
		ExpiresAt:        expiresAt,
		PromptHash:       hash,
		PromptText:       req.Prompt,
		Embedding:        emb,
		ModelID:          req.ModelID,
		Namespace:        ns,
		ResponseText:     req.Response,
		ResponseMeta:     req.ResponseMeta,
		SourceHashes:     req.Sources,
		EmbeddingMissing: embeddingMissing,
	}

	if err := c.store.Put(ctx, entry); err != nil {
		return nil, err
	}

	// Add to vector index if embedding succeeded
	if !embeddingMissing {
		if err := c.vectorIndex.Add(ctx, entry.ID, emb); err != nil {
			c.logger.Error("failed to add vector to index", "error", err)
		}
	}

	c.logger.Info("stored cache entry",
		"entry_id", entry.ID,
		"namespace", ns,
		"sources_count", len(req.Sources))

	return entry, nil
}

// Invalidate manually invalidates all cache entries that depend on the given source ID.
func (c *Client) Invalidate(ctx context.Context, sourceID string) (int, error) {
	count, err := c.invalidator.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    sourceID,
		ContentHash: [32]byte{}, // zero → treat as deletion
		Timestamp:   c.clock.Now(),
	})
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.invalidationsTotal += int64(count)
	c.mu.Unlock()
	return count, nil
}

// InvalidateEntry deletes a single cache entry by ID.
func (c *Client) InvalidateEntry(ctx context.Context, entryID string) error {
	// Remove from vector index
	if err := c.vectorIndex.Delete(ctx, entryID); err != nil {
		c.logger.Error("failed to delete vector", "entry_id", entryID, "error", err)
	}
	return c.store.Delete(ctx, entryID)
}

// Stats returns cache statistics.
func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	storeStats, err := c.store.Stats(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return &Stats{
		TotalEntries:       storeStats.TotalEntries,
		Namespaces:         storeStats.Namespaces,
		ExactHitsTotal:     c.exactHits,
		SemanticHitsTotal:  c.semanticHits,
		MissesTotal:        c.misses,
		InvalidationsTotal: c.invalidationsTotal,
	}, nil
}

// Close shuts down the client and releases resources.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return c.store.Close()
}
