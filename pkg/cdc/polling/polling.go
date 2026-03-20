package polling

import (
	"context"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/org/reverb/pkg/cdc"
)

// HashFunc computes a content hash for the given source ID.
// It is called on each polling interval for every tracked source.
type HashFunc func(ctx context.Context, sourceID string) ([32]byte, error)

// Config holds polling listener configuration.
type Config struct {
	Interval time.Duration
	Sources  []string
	HashFn   HashFunc
}

// Listener implements cdc.Listener by periodically polling sources for changes.
type Listener struct {
	cfg    Config
	logger *slog.Logger

	mu     sync.Mutex
	hashes map[string][32]byte // last known hash per source
}

// New creates a new polling Listener with the given configuration.
func New(cfg Config) *Listener {
	return &Listener{
		cfg:    cfg,
		logger: slog.Default(),
		hashes: make(map[string][32]byte),
	}
}

// Start begins the polling loop. It blocks until the context is canceled.
func (l *Listener) Start(ctx context.Context, events chan<- cdc.ChangeEvent) error {
	ticker := time.NewTicker(l.cfg.Interval)
	defer ticker.Stop()

	// Perform an initial poll immediately to seed the hash map.
	l.poll(ctx, events)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			l.poll(ctx, events)
		}
	}
}

// Name returns the listener name.
func (l *Listener) Name() string {
	return "polling"
}

// poll checks every tracked source and emits a ChangeEvent if the hash differs.
func (l *Listener) poll(ctx context.Context, events chan<- cdc.ChangeEvent) {
	for _, sourceID := range l.cfg.Sources {
		if ctx.Err() != nil {
			return
		}

		newHash, err := l.cfg.HashFn(ctx, sourceID)
		if err != nil {
			l.logger.Error("polling hash failed",
				"source_id", sourceID, "error", err)
			continue
		}

		l.mu.Lock()
		oldHash, exists := l.hashes[sourceID]
		changed := !exists || oldHash != newHash
		if changed {
			l.hashes[sourceID] = newHash
		}
		l.mu.Unlock()

		// Only emit after the initial seed (i.e., when a previous hash existed).
		if changed && exists {
			event := cdc.ChangeEvent{
				SourceID:       sourceID,
				ContentHash:    newHash,
				ContentHashHex: hex.EncodeToString(newHash[:]),
				Timestamp:      time.Now().UTC(),
			}

			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}
