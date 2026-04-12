package lineage_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/lineage"
	"github.com/nobelk/reverb/pkg/store/memory"
)

func TestLineageIndex_AddAndLookup(t *testing.T) {
	s := memory.New()
	idx := lineage.NewIndex(s)
	ctx := context.Background()

	entry := testutil.NewEntry().WithSource("src-A", "content1").Build()
	require.NoError(t, s.Put(ctx, entry))

	ids, err := idx.EntriesForSource(ctx, "src-A")
	require.NoError(t, err)
	assert.Contains(t, ids, entry.ID)
}

func TestLineageIndex_MultipleEntriesPerSource(t *testing.T) {
	s := memory.New()
	idx := lineage.NewIndex(s)
	ctx := context.Background()

	e1 := testutil.NewEntry().WithSource("src-A", "c1").Build()
	e2 := testutil.NewEntry().WithSource("src-A", "c2").Build()
	require.NoError(t, s.Put(ctx, e1))
	require.NoError(t, s.Put(ctx, e2))

	ids, err := idx.EntriesForSource(ctx, "src-A")
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, e1.ID)
	assert.Contains(t, ids, e2.ID)
}

func TestLineageIndex_MultipleSourcesPerEntry(t *testing.T) {
	s := memory.New()
	idx := lineage.NewIndex(s)
	ctx := context.Background()

	entry := testutil.NewEntry().WithSource("src-A", "c1").WithSource("src-B", "c2").Build()
	require.NoError(t, s.Put(ctx, entry))

	idsA, err := idx.EntriesForSource(ctx, "src-A")
	require.NoError(t, err)
	assert.Contains(t, idsA, entry.ID)

	idsB, err := idx.EntriesForSource(ctx, "src-B")
	require.NoError(t, err)
	assert.Contains(t, idsB, entry.ID)
}

func TestLineageIndex_Remove(t *testing.T) {
	s := memory.New()
	idx := lineage.NewIndex(s)
	ctx := context.Background()

	entry := testutil.NewEntry().WithSource("src-A", "c1").Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, s.Delete(ctx, entry.ID))

	ids, err := idx.EntriesForSource(ctx, "src-A")
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestLineageIndex_Empty(t *testing.T) {
	s := memory.New()
	idx := lineage.NewIndex(s)

	ids, err := idx.EntriesForSource(context.Background(), "nonexistent")
	require.NoError(t, err)
	assert.Empty(t, ids)
}
