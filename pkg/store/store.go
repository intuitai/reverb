package store

import (
	"context"
	"time"
)

// CacheEntry is the atomic unit stored by Reverb.
type CacheEntry struct {
	// Identity
	ID        string    // UUIDv7 — sortable by creation time
	CreatedAt time.Time
	ExpiresAt time.Time // hard TTL, zero means no expiry

	// Request fingerprint
	PromptHash    [32]byte  // SHA-256 of normalized prompt text
	PromptText    string    // original prompt (stored for debugging)
	Embedding     []float32 // embedding vector of the prompt
	ModelID       string    // which LLM model produced the response
	Namespace     string    // logical partition

	// Response
	ResponseText string
	ResponseMeta map[string]string // arbitrary metadata

	// Lineage — which source documents contributed to this response
	SourceHashes []SourceRef

	// Bookkeeping
	HitCount  int64
	LastHitAt time.Time

	// Internal flags
	EmbeddingMissing bool // true if embedding failed at Store time; reaper will retry
}

// SourceRef records the identity and content hash of a source document.
type SourceRef struct {
	SourceID    string   // stable identifier for the source document/chunk
	ContentHash [32]byte // SHA-256 of the source content at cache-write time
}

// Store provides durable persistence for cache entries.
type Store interface {
	// Get retrieves a cache entry by ID. Returns nil, nil if not found.
	Get(ctx context.Context, id string) (*CacheEntry, error)

	// GetByHash retrieves a cache entry by prompt hash + namespace.
	// Used by the exact tier. Returns nil, nil if not found.
	GetByHash(ctx context.Context, namespace string, hash [32]byte) (*CacheEntry, error)

	// Put writes a cache entry. Overwrites if ID already exists.
	Put(ctx context.Context, entry *CacheEntry) error

	// Delete removes a cache entry by ID. No-op if not found.
	Delete(ctx context.Context, id string) error

	// DeleteBatch removes multiple cache entries by ID.
	DeleteBatch(ctx context.Context, ids []string) error

	// ListBySource returns all cache entry IDs that reference the given source ID.
	// Used by the invalidation engine.
	ListBySource(ctx context.Context, sourceID string) ([]string, error)

	// IncrementHit updates HitCount and LastHitAt for the given entry.
	IncrementHit(ctx context.Context, id string) error

	// Scan iterates over all entries in a namespace, calling fn for each.
	// Used for background cleanup (expired entries). Return false from fn to stop.
	Scan(ctx context.Context, namespace string, fn func(entry *CacheEntry) bool) error

	// Stats returns aggregate statistics.
	Stats(ctx context.Context) (*StoreStats, error)

	// Close releases resources.
	Close() error
}

// StoreStats holds aggregate statistics about the store.
type StoreStats struct {
	TotalEntries   int64
	TotalSizeBytes int64
	Namespaces     []string
}
