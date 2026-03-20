package exact

import (
	"context"
	"time"

	"github.com/org/reverb/pkg/store"
)

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Cache implements the exact-match (Tier 1) cache.
// It looks up entries by SHA-256 hash of (namespace + normalized_prompt + model_id).
type Cache struct {
	store store.Store
	clock Clock
}

// New creates a new exact-match cache.
func New(s store.Store, clock Clock) *Cache {
	if clock == nil {
		clock = realClock{}
	}
	return &Cache{store: s, clock: clock}
}

// LookupResult holds the result of an exact-match cache lookup.
type LookupResult struct {
	Hit   bool
	Entry *store.CacheEntry
}

// Lookup checks for an exact hash match in the store.
func (c *Cache) Lookup(ctx context.Context, namespace string, hash [32]byte) (*LookupResult, error) {
	entry, err := c.store.GetByHash(ctx, namespace, hash)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return &LookupResult{Hit: false}, nil
	}
	// Check expiry
	if !entry.ExpiresAt.IsZero() && c.clock.Now().After(entry.ExpiresAt) {
		return &LookupResult{Hit: false}, nil
	}
	return &LookupResult{Hit: true, Entry: entry}, nil
}
