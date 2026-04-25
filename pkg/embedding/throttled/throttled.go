// Package throttled wraps an embedding.Provider with a bounded-concurrency
// limiter so the embedding pipeline cannot accumulate unbounded in-flight
// work. When the in-flight cap and bounded queue are both saturated, calls
// fail fast with limiter.ErrOverloaded rather than queueing forever.
package throttled

import (
	"context"

	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/limiter"
	"github.com/nobelk/reverb/pkg/metrics"
)

// Provider decorates an underlying embedding.Provider with a concurrency cap.
// All Provider methods that perform work (Embed, EmbedBatch) acquire a slot
// before delegating; Dimensions is unprotected since it is a pure accessor.
type Provider struct {
	inner embedding.Provider
	cl    *limiter.ConcurrencyLimiter
	prom  *metrics.PrometheusCollector
}

// New returns a wrapped provider. If cl is nil, the returned provider is the
// original inner provider unchanged — this lets callers always wrap without
// branching on whether the concurrency cap is configured.
func New(inner embedding.Provider, cl *limiter.ConcurrencyLimiter, pc *metrics.PrometheusCollector) embedding.Provider {
	if cl == nil {
		return inner
	}
	return &Provider{inner: inner, cl: cl, prom: pc}
}

func (p *Provider) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, err
	}
	defer p.release()
	return p.inner.Embed(ctx, text)
}

func (p *Provider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, err
	}
	defer p.release()
	return p.inner.EmbedBatch(ctx, texts)
}

func (p *Provider) Dimensions() int { return p.inner.Dimensions() }

func (p *Provider) acquire(ctx context.Context) error {
	if err := p.cl.Acquire(ctx); err != nil {
		if p.prom != nil && err == limiter.ErrOverloaded {
			p.prom.RejectedRequestsTotal.WithLabelValues("embedding", "overload").Inc()
		}
		return err
	}
	if p.prom != nil {
		p.prom.EmbeddingInFlight.Set(float64(p.cl.InFlight()))
		p.prom.EmbeddingQueueDepth.Set(float64(p.cl.QueueDepth()))
	}
	return nil
}

func (p *Provider) release() {
	p.cl.Release()
	if p.prom != nil {
		p.prom.EmbeddingInFlight.Set(float64(p.cl.InFlight()))
		p.prom.EmbeddingQueueDepth.Set(float64(p.cl.QueueDepth()))
	}
}
