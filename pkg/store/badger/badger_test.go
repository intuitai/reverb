package badger

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/conformance"
)

func TestBadgerConformance(t *testing.T) {
	conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
		s, err := NewInMemory()
		if err != nil {
			t.Fatalf("failed to create in-memory badger store: %v", err)
		}
		return s
	})
}

// TestBadgerSurvivesClose writes a handful of entries, closes the database,
// reopens it at the same path, and verifies every entry and its indices are
// readable. This is the contract external callers rely on: a clean shutdown
// followed by a restart must preserve all committed state.
//
// Note: Badger's default SyncWrites is false, so power-loss / kernel-panic
// scenarios (which we do not model here) may lose the tail of the WAL. A
// clean Close flushes pending writes.
func TestBadgerSurvivesClose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "badger")

	first, err := New(dir)
	require.NoError(t, err)

	ctx := context.Background()
	// Write three entries with distinct hashes, namespaces, and source refs
	// so the hash-index and lineage-index rehydration are both exercised.
	hashA := [32]byte{1}
	hashB := [32]byte{2}
	hashC := [32]byte{3}

	eA := testutil.NewEntry().WithNamespace("ns1").WithPromptHash(hashA).WithPrompt("alpha").WithResponse("A").WithSource("src-1", "content-1").Build()
	eB := testutil.NewEntry().WithNamespace("ns1").WithPromptHash(hashB).WithPrompt("beta").WithResponse("B").WithSource("src-1", "content-1").Build()
	eC := testutil.NewEntry().WithNamespace("ns2").WithPromptHash(hashC).WithPrompt("gamma").WithResponse("C").Build()

	for _, e := range []*store.CacheEntry{eA, eB, eC} {
		require.NoError(t, first.Put(ctx, e))
	}
	require.NoError(t, first.Close())

	// Reopen the same directory in a fresh process-equivalent.
	second, err := New(dir)
	require.NoError(t, err)
	defer second.Close()

	// 1. Primary entry lookup by ID.
	for _, want := range []*store.CacheEntry{eA, eB, eC} {
		got, err := second.Get(ctx, want.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "entry %s should survive restart", want.ID)
		assert.Equal(t, want.ResponseText, got.ResponseText)
		assert.Equal(t, want.Namespace, got.Namespace)
	}

	// 2. Secondary hash index survives.
	byHash, err := second.GetByHash(ctx, "ns1", hashA)
	require.NoError(t, err)
	require.NotNil(t, byHash)
	assert.Equal(t, eA.ID, byHash.ID)

	// 3. Lineage index survives — both ns1 entries share src-1.
	lineage, err := second.ListBySource(ctx, "src-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{eA.ID, eB.ID}, lineage)

	// 4. Stats reflect the reloaded entries, not a fresh empty index.
	stats, err := second.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalEntries)
	assert.ElementsMatch(t, []string{"ns1", "ns2"}, stats.Namespaces)
}

// Ensure store.Store interface is satisfied (compile-time check).
var _ store.Store = (*Store)(nil)
