package exact_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/cache/exact"
	"github.com/nobelk/reverb/pkg/store/memory"
)

func TestExact_PutAndLookup(t *testing.T) {
	s := memory.New()
	clock := testutil.NewFakeClock(time.Now())
	c := exact.New(s, clock)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns:hello:model"))
	entry := testutil.NewEntry().WithNamespace("ns").WithPrompt("hello").WithResponse("world").Build()
	entry.PromptHash = hash
	require.NoError(t, s.Put(ctx, entry))

	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.True(t, result.Hit)
	assert.Equal(t, entry.ID, result.Entry.ID)
	assert.Equal(t, "world", result.Entry.ResponseText)
}

func TestExact_Miss(t *testing.T) {
	s := memory.New()
	c := exact.New(s, nil)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("nonexistent"))
	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestExact_NamespaceIsolation(t *testing.T) {
	s := memory.New()
	c := exact.New(s, nil)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns-A:hello:model"))
	entry := testutil.NewEntry().WithNamespace("ns-A").WithPrompt("hello").Build()
	entry.PromptHash = hash
	require.NoError(t, s.Put(ctx, entry))

	// Same hash, different namespace → miss
	result, err := c.Lookup(ctx, "ns-B", hash)
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestExact_TTLExpiry(t *testing.T) {
	s := memory.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	c := exact.New(s, clock)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns:test:model"))
	entry := testutil.NewEntry().WithNamespace("ns").WithPrompt("test").Build()
	entry.PromptHash = hash
	entry.ExpiresAt = now.Add(1 * time.Hour)
	require.NoError(t, s.Put(ctx, entry))

	// Before expiry → hit
	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.True(t, result.Hit)

	// Advance past expiry → miss
	clock.Advance(2 * time.Hour)
	result, err = c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestExact_TTLNotExpired(t *testing.T) {
	s := memory.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	c := exact.New(s, clock)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns:test:model"))
	entry := testutil.NewEntry().WithNamespace("ns").WithPrompt("test").Build()
	entry.PromptHash = hash
	entry.ExpiresAt = now.Add(10 * time.Hour)
	require.NoError(t, s.Put(ctx, entry))

	clock.Advance(5 * time.Hour)
	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.True(t, result.Hit)
}

func TestExact_NoTTL(t *testing.T) {
	s := memory.New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	c := exact.New(s, clock)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns:test:model"))
	entry := testutil.NewEntry().WithNamespace("ns").WithPrompt("test").Build()
	entry.PromptHash = hash
	// ExpiresAt is zero → no expiry
	require.NoError(t, s.Put(ctx, entry))

	clock.Advance(1000 * time.Hour)
	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.True(t, result.Hit)
}

func TestExact_Overwrite(t *testing.T) {
	s := memory.New()
	c := exact.New(s, nil)
	ctx := context.Background()

	hash := sha256.Sum256([]byte("ns:test:model"))
	entry := testutil.NewEntry().WithNamespace("ns").WithPrompt("test").WithResponse("v1").Build()
	entry.PromptHash = hash
	require.NoError(t, s.Put(ctx, entry))

	entry.ResponseText = "v2"
	require.NoError(t, s.Put(ctx, entry))

	result, err := c.Lookup(ctx, "ns", hash)
	require.NoError(t, err)
	assert.True(t, result.Hit)
	assert.Equal(t, "v2", result.Entry.ResponseText)
}

func TestExact_ModelIDScoping(t *testing.T) {
	s := memory.New()
	c := exact.New(s, nil)
	ctx := context.Background()

	// Different model IDs → different hashes → different entries
	hashA := sha256.Sum256([]byte("ns:test:model-A"))
	hashB := sha256.Sum256([]byte("ns:test:model-B"))

	entryA := testutil.NewEntry().WithNamespace("ns").WithPrompt("test").WithModelID("model-A").WithResponse("response-A").Build()
	entryA.PromptHash = hashA
	require.NoError(t, s.Put(ctx, entryA))

	// Lookup with model-B hash → miss
	result, err := c.Lookup(ctx, "ns", hashB)
	require.NoError(t, err)
	assert.False(t, result.Hit)

	// Lookup with model-A hash → hit
	result, err = c.Lookup(ctx, "ns", hashA)
	require.NoError(t, err)
	assert.True(t, result.Hit)
	assert.Equal(t, "response-A", result.Entry.ResponseText)
}
