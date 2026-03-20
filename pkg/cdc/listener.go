package cdc

import (
	"context"
	"time"
)

// ChangeEvent represents a source document that has changed.
type ChangeEvent struct {
	SourceID       string    `json:"source_id"`
	ContentHash    [32]byte  `json:"-"`
	ContentHashHex string    `json:"content_hash"` // hex-encoded for JSON
	Timestamp      time.Time `json:"timestamp"`
}

// Listener watches for changes to source documents and emits ChangeEvents.
type Listener interface {
	Start(ctx context.Context, events chan<- ChangeEvent) error
	Name() string
}
