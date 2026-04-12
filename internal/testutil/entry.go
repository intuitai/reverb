package testutil

import (
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"github.com/nobelk/reverb/pkg/store"
)

// EntryBuilder provides a fluent API for constructing CacheEntry values in tests.
type EntryBuilder struct {
	entry store.CacheEntry
}

func NewEntry() *EntryBuilder {
	return &EntryBuilder{
		entry: store.CacheEntry{
			ID:        uuid.New().String(),
			CreatedAt: time.Now(),
			Namespace: "test",
			ModelID:   "test-model",
		},
	}
}

func (b *EntryBuilder) WithNamespace(ns string) *EntryBuilder  { b.entry.Namespace = ns; return b }
func (b *EntryBuilder) WithPrompt(p string) *EntryBuilder      { b.entry.PromptText = p; return b }
func (b *EntryBuilder) WithResponse(r string) *EntryBuilder    { b.entry.ResponseText = r; return b }
func (b *EntryBuilder) WithModelID(m string) *EntryBuilder     { b.entry.ModelID = m; return b }
func (b *EntryBuilder) WithTTL(d time.Duration) *EntryBuilder  { b.entry.ExpiresAt = b.entry.CreatedAt.Add(d); return b }
func (b *EntryBuilder) WithExpiredTTL() *EntryBuilder          { b.entry.ExpiresAt = b.entry.CreatedAt.Add(-1 * time.Hour); return b }
func (b *EntryBuilder) WithExpiresAt(t time.Time) *EntryBuilder { b.entry.ExpiresAt = t; return b }
func (b *EntryBuilder) WithSource(sourceID, content string) *EntryBuilder {
	b.entry.SourceHashes = append(b.entry.SourceHashes, store.SourceRef{
		SourceID:    sourceID,
		ContentHash: sha256.Sum256([]byte(content)),
	})
	return b
}
func (b *EntryBuilder) WithEmbedding(vec []float32) *EntryBuilder { b.entry.Embedding = vec; return b }
func (b *EntryBuilder) WithPromptHash(h [32]byte) *EntryBuilder   { b.entry.PromptHash = h; return b }
func (b *EntryBuilder) Build() *store.CacheEntry                   { return &b.entry }
