package conformance

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/store"
)

// RunStoreConformance runs the full conformance suite against any Store implementation.
func RunStoreConformance(t *testing.T, factory func(t *testing.T) store.Store) {
	t.Run("PutAndGetByID", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().WithPrompt("hello").WithResponse("world").Build()
		require.NoError(t, s.Put(ctx, entry))
		got, err := s.Get(ctx, entry.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, entry.ID, got.ID)
		assert.Equal(t, entry.ResponseText, got.ResponseText)
	})

	t.Run("GetByID_NotFound", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		got, err := s.Get(context.Background(), "nonexistent-id")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("GetByHash", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().WithNamespace("ns1").WithPrompt("test").Build()
		entry.PromptHash = sha256.Sum256([]byte("ns1:test:model"))
		require.NoError(t, s.Put(ctx, entry))
		got, err := s.GetByHash(ctx, "ns1", entry.PromptHash)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, entry.ID, got.ID)
	})

	t.Run("GetByHash_WrongNamespace", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().WithNamespace("ns1").Build()
		entry.PromptHash = sha256.Sum256([]byte("ns1:test"))
		require.NoError(t, s.Put(ctx, entry))
		got, err := s.GetByHash(ctx, "ns2", entry.PromptHash)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("Delete", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().Build()
		require.NoError(t, s.Put(ctx, entry))
		require.NoError(t, s.Delete(ctx, entry.ID))
		got, err := s.Get(ctx, entry.ID)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("Delete_NotFound_NoError", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		err := s.Delete(context.Background(), "nonexistent")
		assert.NoError(t, err)
	})

	t.Run("DeleteBatch", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entries := make([]*store.CacheEntry, 5)
		ids := make([]string, 5)
		for i := range entries {
			entries[i] = testutil.NewEntry().Build()
			ids[i] = entries[i].ID
			require.NoError(t, s.Put(ctx, entries[i]))
		}
		require.NoError(t, s.DeleteBatch(ctx, ids[:3]))
		for i := 0; i < 3; i++ {
			got, _ := s.Get(ctx, ids[i])
			assert.Nil(t, got, "entry %d should be deleted", i)
		}
		for i := 3; i < 5; i++ {
			got, _ := s.Get(ctx, ids[i])
			assert.NotNil(t, got, "entry %d should still exist", i)
		}
	})

	t.Run("ListBySource", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		e1 := testutil.NewEntry().WithSource("src-A", "content1").Build()
		e2 := testutil.NewEntry().WithSource("src-A", "content2").Build()
		e3 := testutil.NewEntry().WithSource("src-B", "content3").Build()
		require.NoError(t, s.Put(ctx, e1))
		require.NoError(t, s.Put(ctx, e2))
		require.NoError(t, s.Put(ctx, e3))
		ids, err := s.ListBySource(ctx, "src-A")
		require.NoError(t, err)
		assert.Len(t, ids, 2)
		assert.Contains(t, ids, e1.ID)
		assert.Contains(t, ids, e2.ID)
	})

	t.Run("ListBySource_Empty", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ids, err := s.ListBySource(context.Background(), "nonexistent-source")
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("IncrementHit", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().Build()
		require.NoError(t, s.Put(ctx, entry))
		require.NoError(t, s.IncrementHit(ctx, entry.ID))
		require.NoError(t, s.IncrementHit(ctx, entry.ID))
		got, _ := s.Get(ctx, entry.ID)
		assert.Equal(t, int64(2), got.HitCount)
		assert.False(t, got.LastHitAt.IsZero())
	})

	t.Run("PutOverwrite", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		entry := testutil.NewEntry().WithResponse("v1").Build()
		require.NoError(t, s.Put(ctx, entry))
		entry.ResponseText = "v2"
		require.NoError(t, s.Put(ctx, entry))
		got, _ := s.Get(ctx, entry.ID)
		assert.Equal(t, "v2", got.ResponseText)
	})

	t.Run("Scan", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		e1 := testutil.NewEntry().WithNamespace("scan-ns").Build()
		e2 := testutil.NewEntry().WithNamespace("scan-ns").Build()
		e3 := testutil.NewEntry().WithNamespace("other-ns").Build()
		require.NoError(t, s.Put(ctx, e1))
		require.NoError(t, s.Put(ctx, e2))
		require.NoError(t, s.Put(ctx, e3))
		var found []string
		err := s.Scan(ctx, "scan-ns", func(entry *store.CacheEntry) bool {
			found = append(found, entry.ID)
			return true
		})
		require.NoError(t, err)
		assert.Len(t, found, 2)
	})

	t.Run("Scan_EarlyStop", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		for i := 0; i < 10; i++ {
			require.NoError(t, s.Put(ctx, testutil.NewEntry().WithNamespace("stop-ns").Build()))
		}
		count := 0
		_ = s.Scan(ctx, "stop-ns", func(_ *store.CacheEntry) bool {
			count++
			return count < 3
		})
		assert.Equal(t, 3, count)
	})

	t.Run("Stats", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx := context.Background()
		require.NoError(t, s.Put(ctx, testutil.NewEntry().WithNamespace("ns1").Build()))
		require.NoError(t, s.Put(ctx, testutil.NewEntry().WithNamespace("ns2").Build()))
		stats, err := s.Stats(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), stats.TotalEntries)
		assert.Len(t, stats.Namespaces, 2)
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		s := factory(t)
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := s.Get(ctx, "any-id")
		assert.Error(t, err)
	})
}
