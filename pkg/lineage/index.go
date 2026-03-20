package lineage

import (
	"context"
	"sync"

	"github.com/org/reverb/pkg/store"
)

// Index tracks the mapping from source documents to cache entries.
// It is used by the invalidation engine to find which cache entries
// need to be invalidated when a source document changes.
type Index struct {
	mu    sync.RWMutex
	store store.Store
}

// NewIndex creates a new lineage index backed by the given store.
func NewIndex(s store.Store) *Index {
	return &Index{store: s}
}

// EntriesForSource returns all cache entry IDs that reference the given source ID.
func (idx *Index) EntriesForSource(ctx context.Context, sourceID string) ([]string, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.store.ListBySource(ctx, sourceID)
}
