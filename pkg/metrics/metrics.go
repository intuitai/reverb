package metrics

import "sync/atomic"

// Collector tracks cache metrics using atomic counters.
type Collector struct {
	ExactHits       atomic.Int64
	SemanticHits    atomic.Int64
	Misses          atomic.Int64
	Stores          atomic.Int64
	Invalidations   atomic.Int64
	EmbeddingErrors atomic.Int64
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (c *Collector) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ExactHits:       c.ExactHits.Load(),
		SemanticHits:    c.SemanticHits.Load(),
		Misses:          c.Misses.Load(),
		Stores:          c.Stores.Load(),
		Invalidations:   c.Invalidations.Load(),
		EmbeddingErrors: c.EmbeddingErrors.Load(),
	}
}

// MetricsSnapshot is a point-in-time snapshot of metrics.
type MetricsSnapshot struct {
	ExactHits       int64
	SemanticHits    int64
	Misses          int64
	Stores          int64
	Invalidations   int64
	EmbeddingErrors int64
}

// HitRate returns the overall cache hit rate.
func (s MetricsSnapshot) HitRate() float64 {
	total := s.ExactHits + s.SemanticHits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.ExactHits+s.SemanticHits) / float64(total)
}
