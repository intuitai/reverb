# Reverb — Developer Design Document

**Semantic Response Cache with Knowledge-Aware Invalidation**

Version: 1.1  
Language: Go 1.22+  
Author: Design Team  
Status: Ready for Implementation

---

## 1. Problem statement

When LLM-powered applications (RAG chatbots, customer-support agents, internal search) serve repeated or semantically identical queries, every query hits the LLM — incurring latency (500ms–5s) and cost ($0.01–$0.15 per call). Naive exact-match caches miss reformulations ("How do I reset my password?" vs "password reset steps"), and no existing cache invalidates entries when the underlying knowledge base changes.

Reverb is a Go library and standalone service that provides a two-tier semantic response cache with automatic, source-aware invalidation.

### 1.1. Goals

- Reduce redundant LLM calls by 30–60% for RAG and conversational workloads.
- Cache hits return in under 10ms (exact tier) or under 50ms (semantic tier).
- Automatically invalidate stale entries when source documents change.
- Ship as both an importable Go library and a standalone HTTP/gRPC service.
- Zero external infrastructure dependencies beyond a configured embedding provider and an optional vector store.
- All tests runnable in containers with no host-level dependencies.

### 1.2. Non-goals

- Reverb is not a general-purpose vector database.
- Reverb does not generate embeddings itself — it calls a configurable embedding provider.
- Reverb does not replace the LLM — it sits between the application and the LLM as middleware.
- Reverb does not handle authentication/authorization — the host application owns that.

---

## 2. Name rationale

**Reverb** — a response that echoes back. When a semantically similar query arrives, Reverb returns the cached answer like an acoustic reverberation of the original. Short, punchy, developer-friendly, and works well in conversation: "Is that answer from Reverb or live?" Captures the core idea that similar queries produce the same echo, while the invalidation engine ensures stale echoes fade when the source changes.

Go module path: `github.com/<org>/reverb`  
CLI binary: `reverb`

---

## 3. Architecture overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        Application                              │
│                                                                 │
│   prompt + metadata ──►  reverb.Lookup(req)                     │
│                              │                                  │
│                    ┌─────────┼─────────┐                        │
│                    │  Tier 1: Exact    │  ◄── SHA-256 of        │
│                    │  (in-memory map   │      normalized prompt  │
│                    │   + optional      │                         │
│                    │   Redis/BadgerDB) │                         │
│                    └────────┬──────────┘                         │
│                        miss │                                    │
│                    ┌────────▼──────────┐                         │
│                    │  Tier 2: Semantic │  ◄── embedding cosine   │
│                    │  (vector index)   │      similarity search  │
│                    └────────┬──────────┘                         │
│                        miss │                                    │
│                             ▼                                    │
│                     call LLM (user code)                         │
│                             │                                    │
│                     reverb.Store(req, resp, sources)             │
│                             │                                    │
│              ┌──────────────┼──────────────┐                     │
│              │ write to     │ write to     │ record source       │
│              │ exact tier   │ semantic tier│ hashes in lineage   │
│              └──────────────┴──────────────┘ index               │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│                   Invalidation Subsystem                         │
│                                                                  │
│   CDC listener (webhook / polling / NATS)                        │
│        │                                                         │
│        ▼                                                         │
│   Source change detected ──► hash changed?                       │
│        │                          │ yes                          │
│        │                          ▼                              │
│        │                  Lineage index lookup:                   │
│        │                  "which cache entries                    │
│        │                   depend on this source?"               │
│        │                          │                              │
│        │                          ▼                              │
│        │                  Invalidate (delete)                    │
│        │                  those entries from                     │
│        │                  both tiers                             │
│        │                                                         │
└──────────────────────────────────────────────────────────────────┘
```

---

## 4. Core concepts

### 4.1. Cache entry

A cache entry is the atomic unit stored by Reverb.

```go
type CacheEntry struct {
    // Identity
    ID        string    // UUIDv7 — sortable by creation time
    CreatedAt time.Time
    ExpiresAt time.Time // hard TTL, zero means no expiry

    // Request fingerprint
    PromptHash    [32]byte  // SHA-256 of normalized prompt text
    PromptText    string    // original prompt (stored for debugging)
    Embedding     []float32 // embedding vector of the prompt
    ModelID       string    // which LLM model produced the response
    Namespace     string    // logical partition (e.g., "support-bot", "internal-search")

    // Response
    ResponseText  string
    ResponseMeta  map[string]string // arbitrary metadata (tokens used, latency, etc.)

    // Lineage — which source documents contributed to this response
    SourceHashes  []SourceRef

    // Bookkeeping
    HitCount      int64
    LastHitAt     time.Time

    // Internal flags
    EmbeddingMissing bool // true if embedding failed at Store time; reaper will retry
}

type SourceRef struct {
    SourceID    string   // stable identifier for the source document/chunk
    ContentHash [32]byte // SHA-256 of the source content at cache-write time
}
```

### 4.2. Namespace

Every cache entry belongs to a **namespace** — a logical partition that scopes lookups and invalidation. A single Reverb instance can serve multiple applications or use cases by using different namespaces. Lookups only search within their namespace.

### 4.3. Similarity threshold

The semantic tier returns a hit only if the cosine similarity between the query embedding and a stored embedding exceeds a configurable threshold. The default is `0.95`, which is deliberately conservative — it catches obvious reformulations while avoiding false positives. Operators can tune this per namespace.

### 4.4. Source lineage

Each cache entry records which source documents (by stable ID and content hash) contributed to the cached response. When a source document changes (its content hash differs from what's recorded), all cache entries referencing that source are invalidated.

---

## 5. Package structure

```
reverb/
├── cmd/
│   └── reverb/                 # Standalone server binary
│       └── main.go
├── pkg/
│   ├── reverb/                 # Public API — the library entrypoint
│   │   ├── client.go           # Reverb client (main facade)
│   │   ├── client_test.go      # Client unit tests (with fakes)
│   │   ├── config.go           # Configuration types and defaults
│   │   ├── config_test.go      # Config validation and defaults tests
│   │   └── options.go          # Functional options for client construction
│   ├── cache/                  # Cache tier implementations
│   │   ├── tier.go             # CacheTier interface
│   │   ├── exact/
│   │   │   ├── exact.go        # Tier 1: exact-match cache
│   │   │   └── exact_test.go
│   │   └── semantic/
│   │   │   ├── semantic.go     # Tier 2: embedding-similarity cache
│   │   │   └── semantic_test.go
│   ├── embedding/              # Embedding provider abstraction
│   │   ├── provider.go         # EmbeddingProvider interface
│   │   ├── fake/
│   │   │   └── fake.go         # Deterministic fake for unit tests
│   │   ├── openai/
│   │   │   ├── openai.go       # OpenAI embeddings implementation
│   │   │   └── openai_test.go  # HTTP mock tests
│   │   └── ollama/
│   │       ├── ollama.go       # Ollama local embeddings implementation
│   │       └── ollama_test.go
│   ├── normalize/              # Prompt normalization
│   │   ├── normalize.go
│   │   └── normalize_test.go
│   ├── lineage/                # Source lineage tracking and invalidation
│   │   ├── index.go            # Lineage index (source → cache entry mapping)
│   │   ├── invalidator.go      # Invalidation engine
│   │   ├── index_test.go
│   │   └── invalidator_test.go
│   ├── cdc/                    # Change-data-capture listeners
│   │   ├── listener.go         # CDCListener interface
│   │   ├── webhook/
│   │   │   ├── webhook.go
│   │   │   └── webhook_test.go
│   │   ├── polling/
│   │   │   ├── polling.go
│   │   │   └── polling_test.go
│   │   └── nats/
│   │       ├── nats.go
│   │       └── nats_test.go
│   ├── store/                  # Persistence backends
│   │   ├── store.go            # Store interface
│   │   ├── conformance/
│   │   │   └── conformance.go  # Shared conformance test suite for all Store impls
│   │   ├── memory/
│   │   │   ├── memory.go
│   │   │   └── memory_test.go
│   │   ├── badger/
│   │   │   ├── badger.go
│   │   │   └── badger_test.go
│   │   └── redis/
│   │       ├── redis.go
│   │       └── redis_test.go
│   ├── vector/                 # Vector index abstraction
│   │   ├── index.go            # VectorIndex interface
│   │   ├── conformance/
│   │   │   └── conformance.go  # Shared conformance test suite for all Index impls
│   │   ├── flat/
│   │   │   ├── flat.go
│   │   │   └── flat_test.go
│   │   └── hnsw/
│   │       ├── hnsw.go
│   │       └── hnsw_test.go
│   ├── server/                 # HTTP + gRPC server (for standalone mode)
│   │   ├── http.go
│   │   ├── http_test.go
│   │   ├── grpc.go
│   │   ├── grpc_test.go
│   │   └── proto/
│   │       └── reverb.proto    # Protobuf service definition
│   └── metrics/                # Observability
│       ├── metrics.go
│       └── tracing.go
├── internal/
│   ├── hashutil/               # Hashing helpers (SHA-256, content hashing)
│   │   ├── hash.go
│   │   └── hash_test.go
│   ├── retry/                  # Exponential backoff with jitter
│   │   ├── retry.go
│   │   └── retry_test.go
│   └── testutil/               # Shared test utilities
│       ├── clock.go            # Controllable clock for TTL tests
│       ├── embedding.go        # Deterministic embedding generator
│       └── entry.go            # CacheEntry builder for tests
├── test/
│   ├── integration/            # End-to-end integration tests
│   │   ├── client_test.go      # Full Client lifecycle tests
│   │   ├── http_test.go        # HTTP API integration tests
│   │   ├── grpc_test.go        # gRPC API integration tests
│   │   ├── cdc_webhook_test.go # CDC webhook integration tests
│   │   ├── redis_test.go       # Redis store integration tests
│   │   └── nats_test.go        # NATS CDC integration tests
│   └── docker-compose.yml      # Test infrastructure (Redis, NATS)
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
├── Dockerfile.test             # Test image with all tools
└── README.md
```

---

## 6. Interface definitions

These are the core interfaces that define Reverb's contract boundaries. Each has multiple implementations that can be swapped via configuration.

### 6.1. Embedding provider

```go
// pkg/embedding/provider.go

package embedding

import "context"

// Provider generates embedding vectors from text.
type Provider interface {
    // Embed returns the embedding vector for a single text input.
    Embed(ctx context.Context, text string) ([]float32, error)

    // EmbedBatch returns embedding vectors for multiple text inputs.
    // Implementations should batch the API call where possible.
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

    // Dimensions returns the dimensionality of the embedding vectors.
    Dimensions() int
}
```

**Implementations:**

| Implementation | Module | Notes |
|---|---|---|
| `openai.Provider` | `pkg/embedding/openai` | Calls OpenAI `/v1/embeddings`. Supports `text-embedding-3-small` (1536d) and `text-embedding-3-large` (3072d). Batches up to 2048 inputs per call. |
| `ollama.Provider` | `pkg/embedding/ollama` | Calls local Ollama instance. Supports any loaded embedding model (e.g., `nomic-embed-text`). No batching — calls are serialized. |
| `fake.Provider` | `pkg/embedding/fake` | Deterministic fake for unit tests. See Section 17. |

### 6.2. Vector index

```go
// pkg/vector/index.go

package vector

import "context"

// SearchResult represents a single result from a similarity search.
type SearchResult struct {
    ID         string
    Score      float32 // cosine similarity, 0.0–1.0
}

// Index provides approximate nearest neighbor search over embedding vectors.
type Index interface {
    // Add inserts a vector with the given ID. If the ID already exists, it is overwritten.
    Add(ctx context.Context, id string, vector []float32) error

    // Search returns the top-k most similar vectors to the query, with similarity >= minScore.
    Search(ctx context.Context, query []float32, k int, minScore float32) ([]SearchResult, error)

    // Delete removes a vector by ID. No-op if not found.
    Delete(ctx context.Context, id string) error

    // Len returns the number of vectors in the index.
    Len() int
}
```

**Implementations:**

| Implementation | Module | Notes |
|---|---|---|
| `flat.Index` | `pkg/vector/flat` | Brute-force linear scan. O(n) search. Suitable for up to ~50K entries. Zero dependencies. Thread-safe via `sync.RWMutex`. |
| `hnsw.Index` | `pkg/vector/hnsw` | Hierarchical Navigable Small World graph. O(log n) search. Suitable for up to ~10M entries. Uses `github.com/viterin/vek` for SIMD-accelerated distance. |

### 6.3. Persistence store

```go
// pkg/store/store.go

package store

import "context"

// Store provides durable persistence for cache entries.
type Store interface {
    // Get retrieves a cache entry by ID. Returns nil, nil if not found.
    Get(ctx context.Context, id string) (*CacheEntry, error)

    // GetByHash retrieves a cache entry by prompt hash + namespace.
    // Used by the exact tier. Returns nil, nil if not found.
    GetByHash(ctx context.Context, namespace string, hash [32]byte) (*CacheEntry, error)

    // Put writes a cache entry. Overwrites if ID already exists.
    Put(ctx context.Context, entry *CacheEntry) error

    // Delete removes a cache entry by ID. No-op if not found.
    Delete(ctx context.Context, id string) error

    // DeleteBatch removes multiple cache entries by ID.
    DeleteBatch(ctx context.Context, ids []string) error

    // ListBySource returns all cache entry IDs that reference the given source ID.
    // Used by the invalidation engine.
    ListBySource(ctx context.Context, sourceID string) ([]string, error)

    // IncrementHit updates HitCount and LastHitAt for the given entry.
    IncrementHit(ctx context.Context, id string) error

    // Scan iterates over all entries in a namespace, calling fn for each.
    // Used for background cleanup (expired entries). Return false from fn to stop.
    Scan(ctx context.Context, namespace string, fn func(entry *CacheEntry) bool) error

    // Stats returns aggregate statistics.
    Stats(ctx context.Context) (*StoreStats, error)

    // Close releases resources.
    Close() error
}

type StoreStats struct {
    TotalEntries   int64
    TotalSizeBytes int64
    Namespaces     []string
}
```

**Implementations:**

| Implementation | Module | Notes |
|---|---|---|
| `memory.Store` | `pkg/store/memory` | `sync.Map`-backed. Ephemeral. For tests and development. Lineage indexed via inverted map `sourceID → []entryID`. |
| `badger.Store` | `pkg/store/badger` | BadgerDB embedded key-value store. Durable, no external deps. Entries stored as protobuf-serialized bytes. Secondary indices for hash lookup and source-lineage lookup via key prefixes. |
| `redis.Store` | `pkg/store/redis` | Redis 7+. Entries in Redis Hashes. Hash-lookup via a sorted set. Source-lineage via Redis Sets (`src:{sourceID} → {entryIDs}`). Supports TTL natively. |

### 6.4. CDC listener

```go
// pkg/cdc/listener.go

package cdc

import "context"

// ChangeEvent represents a source document that has changed.
type ChangeEvent struct {
    SourceID    string    // stable identifier of the source
    ContentHash [32]byte  // new content hash (zero value means deleted)
    Timestamp   time.Time
}

// Listener watches for changes to source documents and emits ChangeEvents.
type Listener interface {
    // Start begins listening. Events are sent to the provided channel.
    // Blocks until ctx is canceled or a fatal error occurs.
    Start(ctx context.Context, events chan<- ChangeEvent) error

    // Name returns a human-readable name for logging.
    Name() string
}
```

**Implementations:**

| Implementation | Module | Trigger |
|---|---|---|
| `webhook.Listener` | `pkg/cdc/webhook` | Exposes `POST /hooks/source-changed` endpoint. The knowledge-base system calls this endpoint when a document is created, updated, or deleted. |
| `polling.Listener` | `pkg/cdc/polling` | Periodically calls a user-provided `HashFunc(ctx, sourceID) ([32]byte, error)` for all tracked sources. Compares against stored hashes. Default interval: 60s. |
| `nats.Listener` | `pkg/cdc/nats` | Subscribes to a NATS JetStream subject (e.g., `reverb.sources.changed`). Expects JSON-encoded `ChangeEvent`. |

---

## 7. Core algorithm: lookup flow

```
Lookup(ctx, LookupRequest) → (LookupResponse, error)

1. NORMALIZE the prompt text:
   a. Unicode NFC normalization
   b. Collapse whitespace (runs of spaces/tabs/newlines → single space)
   c. Lowercase
   d. Strip leading/trailing whitespace
   e. Strip trailing punctuation that doesn't change meaning (trailing "?", ".", "!")

2. EXACT TIER — check for hash match:
   a. Compute SHA-256 of (namespace + normalized_prompt + model_id)
   b. Look up in store via GetByHash()
   c. If found AND not expired AND not invalidated:
      - Increment hit counter (async, fire-and-forget)
      - Return CacheHit{Tier: "exact", Entry: entry, Similarity: 1.0}

3. SEMANTIC TIER — check for embedding similarity:
   a. Compute embedding of the normalized prompt via EmbeddingProvider.Embed()
   b. Search the vector index: Search(embedding, k=5, minScore=threshold)
   c. For each candidate (highest similarity first):
      - Load full entry from store
      - Verify namespace matches
      - Verify model_id matches (if configured to scope by model)
      - Verify not expired
      - If all checks pass:
        - Increment hit counter (async)
        - Return CacheHit{Tier: "semantic", Entry: entry, Similarity: score}

4. MISS — return CacheMiss{}
```

### 7.1. Lookup request/response types

```go
// pkg/reverb/client.go

type LookupRequest struct {
    Namespace string // required
    Prompt    string // required — the raw user prompt
    ModelID   string // optional — scope cache to this model
}

type LookupResponse struct {
    Hit        bool
    Tier       string       // "exact" | "semantic" | ""
    Similarity float32      // 1.0 for exact, 0.0–1.0 for semantic
    Entry      *CacheEntry  // nil on miss
}
```

### 7.2. Store request type

```go
type StoreRequest struct {
    Namespace    string            // required
    Prompt       string            // required
    ModelID      string            // required
    Response     string            // required — the LLM response to cache
    ResponseMeta map[string]string // optional
    Sources      []SourceRef       // required — which sources contributed
    TTL          time.Duration     // optional — override default TTL
}
```

---

## 8. Core algorithm: invalidation flow

```
Invalidation loop (runs as a background goroutine):

1. RECEIVE ChangeEvent from CDC listener channel

2. LOOKUP affected entries:
   a. Query store: ListBySource(event.SourceID)
   b. This returns all cache entry IDs that reference this source

3. For each affected entry:
   a. Load the entry from store
   b. Compare the entry's stored ContentHash for this source against event.ContentHash
   c. If hashes differ (or event.ContentHash is zero, meaning deletion):
      - Delete entry from store
      - Delete entry's vector from the vector index
      - Emit metric: reverb_invalidations_total{source_id, namespace}
      - Log at INFO level: "invalidated entry {id} due to source {source_id} change"
   d. If hashes match:
      - Skip (source changed but this specific entry still references the old content —
        this can happen if the same source was re-hashed to the same value, or if
        the entry was written after the change was already propagated)

4. BATCH optimization: accumulate invalidation deletes and execute as DeleteBatch
   every 100 entries or every 500ms, whichever comes first.
```

### 8.1. Lineage index internals

The lineage index is a secondary index inside the store that maps `sourceID → []entryID`. It is maintained automatically:

- **On Store()**: For each `SourceRef` in the request, add the entry ID to the source's set.
- **On Delete()**: For each `SourceRef` in the entry being deleted, remove the entry ID from the source's set.

For the `memory` store, this is a `map[string]map[string]struct{}` protected by a mutex. For `badger`, it is a key prefix: `lineage:{sourceID}:{entryID} → []byte{}`. For `redis`, it is a Redis Set: `reverb:lineage:{sourceID}`.

---

## 9. Prompt normalization details

The normalizer must be deterministic and must not change the semantic meaning of the prompt. It operates only on surface-level textual noise.

```go
// pkg/normalize/normalize.go

package normalize

// Normalize applies a series of deterministic transformations to reduce
// surface variation between semantically identical prompts.
func Normalize(s string) string {
    // 1. Unicode NFC normalization
    s = norm.NFC.String(s)

    // 2. Lowercase
    s = strings.ToLower(s)

    // 3. Collapse internal whitespace
    s = collapseWhitespace(s) // regexp: \s+ → " "

    // 4. Trim
    s = strings.TrimSpace(s)

    // 5. Strip trailing sentence-ending punctuation
    //    "how do i reset my password?" → "how do i reset my password"
    //    "help!!" → "help"
    //    Does NOT strip internal punctuation or hyphens.
    s = strings.TrimRight(s, ".?!;")

    return s
}
```

This normalization is deliberately conservative. It catches common variations (casing, whitespace, trailing punctuation) without risking semantic changes. More aggressive normalization (stopword removal, lemmatization) is intentionally omitted because it can change meaning ("not good" → "good" after stopword removal).

---

## 10. Configuration

Reverb is configured via a Go struct, which can be hydrated from a YAML/JSON file, environment variables, or programmatic construction.

```go
// pkg/reverb/config.go

package reverb

type Config struct {
    // Namespace defaults
    DefaultNamespace string        `yaml:"default_namespace" env:"REVERB_DEFAULT_NAMESPACE"`
    DefaultTTL       time.Duration `yaml:"default_ttl" env:"REVERB_DEFAULT_TTL"`

    // Semantic tier
    SimilarityThreshold float32 `yaml:"similarity_threshold" env:"REVERB_SIMILARITY_THRESHOLD"`
    SemanticTopK        int     `yaml:"semantic_top_k" env:"REVERB_SEMANTIC_TOP_K"`
    ScopeByModel        bool    `yaml:"scope_by_model" env:"REVERB_SCOPE_BY_MODEL"`

    // Embedding provider
    Embedding EmbeddingConfig `yaml:"embedding"`

    // Store backend
    Store StoreConfig `yaml:"store"`

    // Vector index
    Vector VectorConfig `yaml:"vector"`

    // CDC / Invalidation
    CDC CDCConfig `yaml:"cdc"`

    // Server (standalone mode only)
    Server ServerConfig `yaml:"server"`

    // Observability
    Metrics MetricsConfig `yaml:"metrics"`

    // Clock — injectable for tests (defaults to real time)
    Clock Clock `yaml:"-"`
}

// Clock abstracts time for testability.
type Clock interface {
    Now() time.Time
}

type EmbeddingConfig struct {
    Provider   string `yaml:"provider"`    // "openai" | "ollama"
    Model      string `yaml:"model"`       // e.g., "text-embedding-3-small"
    APIKey     string `yaml:"api_key" env:"REVERB_EMBEDDING_API_KEY"`
    BaseURL    string `yaml:"base_url"`    // override endpoint (for proxies, Azure, etc.)
    Dimensions int    `yaml:"dimensions"`  // override if model supports variable dims
}

type StoreConfig struct {
    Backend string `yaml:"backend"` // "memory" | "badger" | "redis"

    // BadgerDB options
    BadgerPath string `yaml:"badger_path"`

    // Redis options
    RedisAddr     string `yaml:"redis_addr"`
    RedisPassword string `yaml:"redis_password" env:"REVERB_REDIS_PASSWORD"`
    RedisDB       int    `yaml:"redis_db"`
    RedisPrefix   string `yaml:"redis_prefix"` // key prefix, default "reverb:"
}

type VectorConfig struct {
    Backend  string `yaml:"backend"` // "flat" | "hnsw"

    // HNSW options
    HNSWm            int `yaml:"hnsw_m"`              // max connections per node, default 16
    HNSWefConstruct  int `yaml:"hnsw_ef_construction"` // default 200
    HNSWefSearch     int `yaml:"hnsw_ef_search"`       // default 100
}

type CDCConfig struct {
    Enabled  bool   `yaml:"enabled"`
    Mode     string `yaml:"mode"`     // "webhook" | "polling" | "nats"

    // Webhook options
    WebhookAddr string `yaml:"webhook_addr"` // e.g., ":9091"
    WebhookPath string `yaml:"webhook_path"` // e.g., "/hooks/source-changed"

    // Polling options
    PollInterval time.Duration `yaml:"poll_interval"`

    // NATS options
    NatsURL     string `yaml:"nats_url"`
    NatsSubject string `yaml:"nats_subject"`
}

type ServerConfig struct {
    HTTPAddr string `yaml:"http_addr"` // e.g., ":8080"
    GRPCAddr string `yaml:"grpc_addr"` // e.g., ":9090"
}

type MetricsConfig struct {
    Enabled bool   `yaml:"enabled"`
    Addr    string `yaml:"addr"` // Prometheus endpoint, e.g., ":9100"
}
```

### 10.1. Default values

```yaml
# reverb.yaml — example configuration with defaults annotated
default_namespace: "default"
default_ttl: 24h
similarity_threshold: 0.95
semantic_top_k: 5
scope_by_model: true

embedding:
  provider: "openai"
  model: "text-embedding-3-small"

store:
  backend: "badger"
  badger_path: "./data/reverb.db"

vector:
  backend: "hnsw"
  hnsw_m: 16
  hnsw_ef_construction: 200
  hnsw_ef_search: 100

cdc:
  enabled: true
  mode: "webhook"
  webhook_addr: ":9091"
  webhook_path: "/hooks/source-changed"

server:
  http_addr: ":8080"
  grpc_addr: ":9090"

metrics:
  enabled: true
  addr: ":9100"
```

---

## 11. Public API — the `reverb.Client` facade

This is the primary interface that application code interacts with.

```go
// pkg/reverb/client.go

package reverb

import "context"

// Client is the primary entry point for Reverb.
// It is safe for concurrent use.
type Client struct {
    cfg         Config
    embedder    embedding.Provider
    exactTier   *exact.Cache
    semanticTier *semantic.Cache
    store       store.Store
    vectorIndex vector.Index
    invalidator *lineage.Invalidator
    metrics     *metrics.Collector
    clock       Clock
}

// New creates a new Reverb client with the given configuration.
// It initializes the store, vector index, embedding provider,
// and optionally starts the CDC listener and invalidation loop.
func New(ctx context.Context, cfg Config) (*Client, error) { ... }

// Lookup checks the cache for a matching response.
// It checks the exact tier first, then the semantic tier.
// Returns a LookupResponse indicating hit/miss and the cached entry if found.
func (c *Client) Lookup(ctx context.Context, req LookupRequest) (*LookupResponse, error) { ... }

// Store writes a new cache entry for the given prompt and response.
// The sources parameter records which source documents contributed to
// this response, enabling knowledge-aware invalidation.
func (c *Client) Store(ctx context.Context, req StoreRequest) (*CacheEntry, error) { ... }

// Invalidate manually invalidates all cache entries that depend on the
// given source ID. Use this when the CDC listener is not enabled and
// you want to trigger invalidation programmatically.
func (c *Client) Invalidate(ctx context.Context, sourceID string) (int, error) { ... }

// InvalidateEntry deletes a single cache entry by ID.
func (c *Client) InvalidateEntry(ctx context.Context, entryID string) error { ... }

// Stats returns cache statistics (total entries, hit rates, invalidation counts).
func (c *Client) Stats(ctx context.Context) (*Stats, error) { ... }

// Close shuts down the client, stops background goroutines,
// flushes pending writes, and releases resources.
func (c *Client) Close() error { ... }
```

### 11.1. Usage example — library mode

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/nobelk/reverb/pkg/reverb"
)

func main() {
    ctx := context.Background()

    client, err := reverb.New(ctx, reverb.Config{
        DefaultNamespace:    "support-bot",
        DefaultTTL:          24 * time.Hour,
        SimilarityThreshold: 0.95,
        Embedding: reverb.EmbeddingConfig{
            Provider: "openai",
            Model:    "text-embedding-3-small",
        },
        Store: reverb.StoreConfig{
            Backend:    "badger",
            BadgerPath: "./data/reverb.db",
        },
        Vector: reverb.VectorConfig{
            Backend: "hnsw",
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    // --- Lookup ---
    resp, err := client.Lookup(ctx, reverb.LookupRequest{
        Namespace: "support-bot",
        Prompt:    "How do I reset my password?",
        ModelID:   "gpt-4o",
    })
    if err != nil {
        log.Fatal(err)
    }

    if resp.Hit {
        fmt.Printf("Cache %s hit (similarity: %.3f): %s\n",
            resp.Tier, resp.Similarity, resp.Entry.ResponseText)
        return
    }

    // --- Cache miss: call LLM (your code) ---
    llmResponse := callLLM("How do I reset my password?")

    // --- Store ---
    _, err = client.Store(ctx, reverb.StoreRequest{
        Namespace: "support-bot",
        Prompt:    "How do I reset my password?",
        ModelID:   "gpt-4o",
        Response:  llmResponse,
        Sources: []reverb.SourceRef{
            {SourceID: "doc:password-reset-guide", ContentHash: hashOf(guideContent)},
            {SourceID: "doc:account-faq",          ContentHash: hashOf(faqContent)},
        },
    })
    if err != nil {
        log.Fatal(err)
    }
}
```

---

## 12. HTTP API — standalone server mode

When run as a standalone service (`cmd/reverb/main.go`), Reverb exposes the following HTTP endpoints.

### 12.1. `POST /v1/lookup`

**Request:**
```json
{
  "namespace": "support-bot",
  "prompt": "How do I reset my password?",
  "model_id": "gpt-4o"
}
```

**Response (hit):**
```json
{
  "hit": true,
  "tier": "semantic",
  "similarity": 0.972,
  "entry": {
    "id": "01912345-abcd-7000-8000-000000000001",
    "prompt_text": "password reset steps",
    "response_text": "To reset your password, go to Settings...",
    "model_id": "gpt-4o",
    "created_at": "2026-03-19T10:30:00Z",
    "hit_count": 14,
    "source_hashes": [
      {"source_id": "doc:password-reset-guide", "content_hash": "a1b2c3..."}
    ]
  }
}
```

**Response (miss):**
```json
{
  "hit": false,
  "tier": "",
  "similarity": 0.0,
  "entry": null
}
```

### 12.2. `POST /v1/store`

**Request:**
```json
{
  "namespace": "support-bot",
  "prompt": "How do I reset my password?",
  "model_id": "gpt-4o",
  "response": "To reset your password, go to Settings...",
  "response_meta": {"tokens_used": "142", "latency_ms": "890"},
  "sources": [
    {"source_id": "doc:password-reset-guide", "content_hash": "a1b2c3..."}
  ],
  "ttl_seconds": 86400
}
```

**Response:**
```json
{
  "id": "01912345-abcd-7000-8000-000000000002",
  "created_at": "2026-03-20T08:15:00Z"
}
```

### 12.3. `POST /v1/invalidate`

**Request:**
```json
{
  "source_id": "doc:password-reset-guide"
}
```

**Response:**
```json
{
  "invalidated_count": 3
}
```

### 12.4. `DELETE /v1/entries/{id}`

Deletes a single cache entry.

### 12.5. `GET /v1/stats`

Returns aggregate statistics:
```json
{
  "total_entries": 12847,
  "namespaces": ["support-bot", "internal-search"],
  "exact_hits_total": 45012,
  "semantic_hits_total": 18934,
  "misses_total": 22156,
  "invalidations_total": 891,
  "hit_rate": 0.742
}
```

### 12.6. `GET /healthz`

Returns `200 OK` with `{"status": "ok"}`.

---

## 13. gRPC API

The protobuf service definition mirrors the HTTP API.

```protobuf
// pkg/server/proto/reverb.proto

syntax = "proto3";
package reverb.v1;
option go_package = "github.com/nobelk/reverb/pkg/server/proto";

service ReverbService {
  rpc Lookup(LookupRequest) returns (LookupResponse);
  rpc Store(StoreRequest) returns (StoreResponse);
  rpc Invalidate(InvalidateRequest) returns (InvalidateResponse);
  rpc DeleteEntry(DeleteEntryRequest) returns (DeleteEntryResponse);
  rpc GetStats(GetStatsRequest) returns (GetStatsResponse);
}

message LookupRequest {
  string namespace = 1;
  string prompt = 2;
  string model_id = 3;
}

message LookupResponse {
  bool hit = 1;
  string tier = 2;
  float similarity = 3;
  CacheEntry entry = 4;
}

message CacheEntry {
  string id = 1;
  string prompt_text = 2;
  string response_text = 3;
  string model_id = 4;
  string namespace = 5;
  int64 created_at_unix = 6;
  int64 hit_count = 7;
  repeated SourceRef source_hashes = 8;
  map<string, string> response_meta = 9;
}

message SourceRef {
  string source_id = 1;
  bytes content_hash = 2;
}

message StoreRequest {
  string namespace = 1;
  string prompt = 2;
  string model_id = 3;
  string response = 4;
  map<string, string> response_meta = 5;
  repeated SourceRef sources = 6;
  int64 ttl_seconds = 7;
}

message StoreResponse {
  string id = 1;
  int64 created_at_unix = 2;
}

message InvalidateRequest {
  string source_id = 1;
}

message InvalidateResponse {
  int32 invalidated_count = 1;
}

message DeleteEntryRequest {
  string id = 1;
}

message DeleteEntryResponse {}

message GetStatsRequest {}

message GetStatsResponse {
  int64 total_entries = 1;
  repeated string namespaces = 2;
  int64 exact_hits_total = 3;
  int64 semantic_hits_total = 4;
  int64 misses_total = 5;
  int64 invalidations_total = 6;
  double hit_rate = 7;
}
```

---

## 14. Observability

### 14.1. Prometheus metrics

All metrics are prefixed with `reverb_`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `reverb_lookups_total` | Counter | `namespace`, `tier` (`exact`, `semantic`, `miss`) | Total lookups by outcome |
| `reverb_lookup_duration_seconds` | Histogram | `namespace`, `tier` | Latency of lookup operations |
| `reverb_stores_total` | Counter | `namespace` | Total store operations |
| `reverb_store_duration_seconds` | Histogram | `namespace` | Latency of store operations |
| `reverb_invalidations_total` | Counter | `namespace`, `source_id` | Cache entries invalidated |
| `reverb_entries_total` | Gauge | `namespace` | Current number of cache entries |
| `reverb_embedding_duration_seconds` | Histogram | `provider` | Latency of embedding API calls |
| `reverb_embedding_errors_total` | Counter | `provider` | Embedding API errors |
| `reverb_vector_search_duration_seconds` | Histogram | | Vector index search latency |
| `reverb_hit_rate` | Gauge | `namespace` | Rolling hit rate (computed every 60s) |

### 14.2. OpenTelemetry tracing

Every `Lookup` and `Store` call creates a span:

- `reverb.lookup` — with attributes: `reverb.namespace`, `reverb.tier`, `reverb.hit`, `reverb.similarity`
- `reverb.store` — with attributes: `reverb.namespace`, `reverb.entry_id`, `reverb.sources_count`
- `reverb.invalidate` — with attributes: `reverb.source_id`, `reverb.invalidated_count`

Child spans are created for `embedding.embed`, `vector.search`, `store.get`, `store.put`.

### 14.3. Structured logging

Reverb uses `log/slog` (standard library, Go 1.21+). Key events:

- `INFO` on cache hit (with tier, similarity, entry ID)
- `INFO` on invalidation (with source ID, entry count)
- `WARN` on embedding API errors (with retry count)
- `ERROR` on store failures

---

## 15. Concurrency model

### 15.1. Thread safety

- The `Client` struct is safe for concurrent use from multiple goroutines.
- The `flat.Index` uses `sync.RWMutex` (read-lock for search, write-lock for add/delete).
- The `hnsw.Index` uses internal locking from the underlying HNSW library.
- The `memory.Store` uses `sync.Map` for the primary map and `sync.RWMutex` for the lineage index.
- The `badger.Store` relies on BadgerDB's built-in concurrency control.
- The `redis.Store` relies on Redis's single-threaded execution model.
- Hit counter increments are fire-and-forget goroutines to avoid blocking the lookup path.

### 15.2. Background goroutines

The `Client` starts the following background goroutines (all stopped on `Close()`):

| Goroutine | Purpose | Interval |
|---|---|---|
| `invalidationLoop` | Reads from the CDC event channel and processes invalidations | Continuous (event-driven) |
| `expiryReaper` | Scans for expired entries and deletes them | Every 5 minutes (configurable) |
| `metricsUpdater` | Recomputes rolling hit rate and entries gauge | Every 60 seconds |
| `cdcListener.Start` | The CDC listener goroutine (webhook server, poller, or NATS subscriber) | Depends on mode |

All goroutines respect the context passed to `New()` and shut down cleanly on `Close()`.

---

## 16. Error handling strategy

### 16.1. Embedding failures

If the embedding provider fails during `Lookup`, the semantic tier is skipped and only the exact tier is checked. An error is logged, the `reverb_embedding_errors_total` metric is incremented, and the lookup returns normally (as a miss from the semantic tier). This is a graceful degradation — the cache still works, just with reduced hit rate.

If the embedding provider fails during `Store`, the entry is stored in the exact tier only (hash-based). The vector index is not updated. A warning is logged. The next time the same prompt is looked up, it will hit the exact tier. The entry is marked with `EmbeddingMissing: true` so the background reaper can retry embedding later.

### 16.2. Store failures

If the persistence store fails during `Lookup`, the lookup returns an error. The caller should fall through to the LLM.

If the persistence store fails during `Store`, the store returns an error. The caller should log it and proceed — the LLM response has already been returned to the user; the cache miss on the next identical query is acceptable.

### 16.3. Retry policy

External calls (embedding API, Redis) use exponential backoff with jitter:

- Initial delay: 100ms
- Max delay: 5s
- Max retries: 3
- Jitter: ±25%

Retries are implemented via a shared `internal/retry` package.

---

## 17. Testing strategy

### 17.1. Test doubles

Every external dependency has a test double that lives in the repository. These enable fast, deterministic unit tests with zero network calls.

#### 17.1.1. Fake embedding provider

```go
// pkg/embedding/fake/fake.go

package fake

import (
    "context"
    "hash/fnv"
    "math"
)

// Provider is a deterministic embedding provider for tests.
// It generates embeddings by hashing the input text into a fixed-dimension
// vector. Semantically identical inputs always produce identical embeddings.
// Different inputs produce different embeddings with cosine similarity
// that correlates loosely with string edit distance (sufficient for
// testing threshold logic).
type Provider struct {
    dims int
}

func New(dims int) *Provider {
    return &Provider{dims: dims}
}

func (p *Provider) Embed(_ context.Context, text string) ([]float32, error) {
    return p.hashToVector(text), nil
}

func (p *Provider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
    result := make([][]float32, len(texts))
    for i, t := range texts {
        result[i] = p.hashToVector(t)
    }
    return result, nil
}

func (p *Provider) Dimensions() int { return p.dims }

// hashToVector produces a deterministic, L2-normalized vector from text.
// Uses FNV-1a hash seeded at multiple offsets to fill the dimensions.
func (p *Provider) hashToVector(text string) []float32 {
    vec := make([]float32, p.dims)
    for i := range vec {
        h := fnv.New64a()
        h.Write([]byte{byte(i), byte(i >> 8)})
        h.Write([]byte(text))
        bits := h.Sum64()
        vec[i] = float32(bits&0xFFFF) / 0xFFFF // [0, 1]
    }
    // L2 normalize
    var norm float64
    for _, v := range vec {
        norm += float64(v) * float64(v)
    }
    norm = math.Sqrt(norm)
    if norm > 0 {
        for i := range vec {
            vec[i] = float32(float64(vec[i]) / norm)
        }
    }
    return vec
}
```

#### 17.1.2. Failing embedding provider (for error path tests)

```go
// pkg/embedding/fake/failing.go

package fake

import (
    "context"
    "errors"
)

// FailingProvider always returns an error. Used to test graceful degradation.
type FailingProvider struct {
    Err  error
    dims int
}

func NewFailing(dims int, err error) *FailingProvider {
    if err == nil {
        err = errors.New("fake embedding failure")
    }
    return &FailingProvider{Err: err, dims: dims}
}

func (p *FailingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
    return nil, p.Err
}

func (p *FailingProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
    return nil, p.Err
}

func (p *FailingProvider) Dimensions() int { return p.dims }
```

#### 17.1.3. Controllable clock

```go
// internal/testutil/clock.go

package testutil

import (
    "sync"
    "time"
)

// FakeClock is a manually controllable clock for testing TTL and expiry logic.
type FakeClock struct {
    mu  sync.Mutex
    now time.Time
}

func NewFakeClock(start time.Time) *FakeClock {
    return &FakeClock{now: start}
}

func (c *FakeClock) Now() time.Time {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.now
}

func (c *FakeClock) Advance(d time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.now = c.now.Add(d)
}

func (c *FakeClock) Set(t time.Time) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.now = t
}
```

#### 17.1.4. Cache entry test builder

```go
// internal/testutil/entry.go

package testutil

import (
    "crypto/sha256"
    "time"

    "github.com/google/uuid"
)

// EntryBuilder provides a fluent API for constructing CacheEntry values in tests.
type EntryBuilder struct {
    entry CacheEntry
}

func NewEntry() *EntryBuilder {
    return &EntryBuilder{
        entry: CacheEntry{
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
func (b *EntryBuilder) WithSource(sourceID, content string) *EntryBuilder {
    b.entry.SourceHashes = append(b.entry.SourceHashes, SourceRef{
        SourceID:    sourceID,
        ContentHash: sha256.Sum256([]byte(content)),
    })
    return b
}
func (b *EntryBuilder) WithEmbedding(vec []float32) *EntryBuilder { b.entry.Embedding = vec; return b }
func (b *EntryBuilder) Build() *CacheEntry                       { return &b.entry }
```

### 17.2. Conformance test suites

Every pluggable interface has a shared conformance test suite that all implementations must pass. This ensures consistent behavior across backends.

#### 17.2.1. Store conformance suite

```go
// pkg/store/conformance/conformance.go

package conformance

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// RunStoreConformance runs the full conformance suite against any Store implementation.
// The factory function must return a fresh, empty store for each subtest.
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
        entries := make([]*CacheEntry, 5)
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
        err := s.Scan(ctx, "scan-ns", func(entry *CacheEntry) bool {
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
        _ = s.Scan(ctx, "stop-ns", func(_ *CacheEntry) bool {
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
        cancel() // cancel immediately
        _, err := s.Get(ctx, "any-id")
        assert.Error(t, err)
    })
}
```

Each Store implementation calls this suite:

```go
// pkg/store/memory/memory_test.go

func TestMemoryStoreConformance(t *testing.T) {
    conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
        return memory.New()
    })
}

// pkg/store/badger/badger_test.go

func TestBadgerStoreConformance(t *testing.T) {
    conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
        dir := t.TempDir()
        s, err := badger.New(dir)
        require.NoError(t, err)
        return s
    })
}

// pkg/store/redis/redis_test.go (integration test, requires Redis)
//go:build integration

func TestRedisStoreConformance(t *testing.T) {
    addr := os.Getenv("REVERB_TEST_REDIS_ADDR")
    if addr == "" {
        addr = "localhost:6379"
    }
    conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
        prefix := fmt.Sprintf("reverb-test-%s:", uuid.New().String()[:8])
        s, err := redis.New(addr, "", 0, prefix)
        require.NoError(t, err)
        t.Cleanup(func() { s.FlushPrefix(context.Background()); s.Close() })
        return s
    })
}
```

#### 17.2.2. Vector index conformance suite

```go
// pkg/vector/conformance/conformance.go

package conformance

func RunVectorIndexConformance(t *testing.T, factory func(t *testing.T, dims int) vector.Index) {
    dims := 8

    t.Run("AddAndSearchExactMatch", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        vec := randomVector(dims)
        require.NoError(t, idx.Add(ctx, "v1", vec))
        results, err := idx.Search(ctx, vec, 1, 0.99)
        require.NoError(t, err)
        require.Len(t, results, 1)
        assert.Equal(t, "v1", results[0].ID)
        assert.InDelta(t, 1.0, results[0].Score, 0.01)
    })

    t.Run("SearchRespectsMinScore", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        require.NoError(t, idx.Add(ctx, "v1", randomVector(dims)))
        // Search with an orthogonal vector — should not match at high threshold
        results, _ := idx.Search(ctx, orthogonalVector(dims), 5, 0.99)
        assert.Empty(t, results)
    })

    t.Run("SearchTopK", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        for i := 0; i < 20; i++ {
            require.NoError(t, idx.Add(ctx, fmt.Sprintf("v%d", i), randomVector(dims)))
        }
        results, err := idx.Search(ctx, randomVector(dims), 3, 0.0)
        require.NoError(t, err)
        assert.LessOrEqual(t, len(results), 3)
    })

    t.Run("SearchResultsOrderedByScore", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        for i := 0; i < 50; i++ {
            require.NoError(t, idx.Add(ctx, fmt.Sprintf("v%d", i), randomVector(dims)))
        }
        results, _ := idx.Search(ctx, randomVector(dims), 10, 0.0)
        for i := 1; i < len(results); i++ {
            assert.GreaterOrEqual(t, results[i-1].Score, results[i].Score,
                "results should be sorted descending by score")
        }
    })

    t.Run("Delete", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        vec := randomVector(dims)
        require.NoError(t, idx.Add(ctx, "v1", vec))
        require.NoError(t, idx.Delete(ctx, "v1"))
        results, _ := idx.Search(ctx, vec, 1, 0.99)
        assert.Empty(t, results)
    })

    t.Run("DeleteNonexistent_NoError", func(t *testing.T) {
        idx := factory(t, dims)
        err := idx.Delete(context.Background(), "nonexistent")
        assert.NoError(t, err)
    })

    t.Run("Len", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        assert.Equal(t, 0, idx.Len())
        require.NoError(t, idx.Add(ctx, "v1", randomVector(dims)))
        require.NoError(t, idx.Add(ctx, "v2", randomVector(dims)))
        assert.Equal(t, 2, idx.Len())
        require.NoError(t, idx.Delete(ctx, "v1"))
        assert.Equal(t, 1, idx.Len())
    })

    t.Run("AddOverwrite", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        vec1 := randomVector(dims)
        vec2 := randomVector(dims)
        require.NoError(t, idx.Add(ctx, "v1", vec1))
        require.NoError(t, idx.Add(ctx, "v1", vec2))
        assert.Equal(t, 1, idx.Len(), "overwrite should not create duplicate")
        results, _ := idx.Search(ctx, vec2, 1, 0.99)
        require.Len(t, results, 1)
        assert.Equal(t, "v1", results[0].ID)
    })

    t.Run("EmptyIndex", func(t *testing.T) {
        idx := factory(t, dims)
        results, err := idx.Search(context.Background(), randomVector(dims), 5, 0.0)
        require.NoError(t, err)
        assert.Empty(t, results)
    })

    t.Run("ConcurrentAccess", func(t *testing.T) {
        idx := factory(t, dims)
        ctx := context.Background()
        var wg sync.WaitGroup
        for i := 0; i < 100; i++ {
            wg.Add(1)
            go func(i int) {
                defer wg.Done()
                id := fmt.Sprintf("v%d", i)
                _ = idx.Add(ctx, id, randomVector(dims))
                _, _ = idx.Search(ctx, randomVector(dims), 3, 0.0)
                if i%3 == 0 {
                    _ = idx.Delete(ctx, id)
                }
            }(i)
        }
        wg.Wait()
    })
}
```

### 17.3. Unit tests — per-package details

Every package has `_test.go` files with table-driven tests. Below is the exhaustive list of test scenarios per package.

#### `internal/hashutil`

| Test | Description |
|---|---|
| `TestSHA256_Deterministic` | Same input always produces same hash |
| `TestSHA256_DifferentInputs` | Different inputs produce different hashes |
| `TestSHA256_EmptyString` | Empty string produces a valid 32-byte hash |
| `TestContentHash_LargeInput` | 10MB string hashes without error |

#### `internal/retry`

| Test | Description |
|---|---|
| `TestRetry_SucceedsImmediately` | No retry needed when fn succeeds on first call |
| `TestRetry_SucceedsAfterRetries` | fn fails twice, succeeds on third call |
| `TestRetry_ExhaustsRetries` | fn always fails; error returned after max retries |
| `TestRetry_ExponentialBackoff` | Verify delays double between retries (±jitter tolerance) |
| `TestRetry_ContextCancellation` | Retry stops immediately when context is canceled |
| `TestRetry_JitterRange` | Delay jitter stays within ±25% of base |

#### `pkg/normalize`

| Test | Description |
|---|---|
| `TestNormalize_LowercaseConversion` | `"HOW DO I Reset"` → `"how do i reset"` |
| `TestNormalize_WhitespaceCollapse` | `"hello   world\t\nfoo"` → `"hello world foo"` |
| `TestNormalize_TrailingPunctuation` | `"reset my password?"` → `"reset my password"` |
| `TestNormalize_MultiplePunctuation` | `"help!!!"` → `"help"` |
| `TestNormalize_InternalPunctuation` | `"it's a semi-colon; test"` — internal punctuation preserved |
| `TestNormalize_UnicodeNFC` | NFC normalization of composed vs decomposed chars |
| `TestNormalize_CJKCharacters` | CJK input passes through without corruption |
| `TestNormalize_EmptyString` | `""` → `""` |
| `TestNormalize_OnlyPunctuation` | `"???"` → `""` |
| `TestNormalize_OnlyWhitespace` | `"   \t  "` → `""` |
| `TestNormalize_LeadingTrailingSpaces` | `"  hello  "` → `"hello"` |
| `TestNormalize_Idempotent` | `Normalize(Normalize(s)) == Normalize(s)` for random inputs |

#### `pkg/cache/exact`

| Test | Description |
|---|---|
| `TestExact_PutAndLookup` | Store entry, look up same hash → hit |
| `TestExact_Miss` | Look up non-existent hash → miss |
| `TestExact_NamespaceIsolation` | Same hash, different namespace → miss |
| `TestExact_TTLExpiry` | Store with short TTL, advance clock past expiry → miss |
| `TestExact_TTLNotExpired` | Store with long TTL, advance clock slightly → hit |
| `TestExact_NoTTL` | Store with zero TTL → always hit (no expiry) |
| `TestExact_Overwrite` | Store same hash twice → second value returned |
| `TestExact_ModelIDScoping` | Same prompt, different model_id → different entries when scope_by_model=true |

#### `pkg/cache/semantic`

| Test | Description |
|---|---|
| `TestSemantic_ExactVectorMatch` | Store vector, search identical vector → hit with similarity ~1.0 |
| `TestSemantic_SimilarVector` | Store vector, search nearby vector → hit above threshold |
| `TestSemantic_BelowThreshold` | Store vector, search distant vector → miss |
| `TestSemantic_ThresholdBoundary` | Test with similarity exactly at threshold (within float tolerance) |
| `TestSemantic_TopKRanking` | Store 10 vectors, search → top-k ordered by similarity descending |
| `TestSemantic_NamespaceFilter` | Verify results are filtered to correct namespace |
| `TestSemantic_ModelFilter` | Verify results are filtered by model_id when scoping is enabled |
| `TestSemantic_ExpiredFiltered` | Expired entries excluded from semantic results |
| `TestSemantic_EmbeddingFailure_GracefulMiss` | Embedding provider fails → return miss, no error |

#### `pkg/lineage`

| Test | Description |
|---|---|
| `TestLineageIndex_AddAndLookup` | Add source mapping, list by source → returns entry IDs |
| `TestLineageIndex_MultipleEntriesPerSource` | Multiple entries referencing same source |
| `TestLineageIndex_MultipleSourcesPerEntry` | One entry referencing multiple sources |
| `TestLineageIndex_Remove` | Remove entry from lineage index after delete |
| `TestLineageIndex_Empty` | List non-existent source → empty slice |
| `TestInvalidator_InvalidateOnHashChange` | Source hash changes → entries deleted |
| `TestInvalidator_NoInvalidateOnSameHash` | Source hash unchanged → entries kept |
| `TestInvalidator_InvalidateOnDeletion` | Zero-value hash (deletion) → entries deleted |
| `TestInvalidator_BatchAccumulation` | Multiple events batch into single DeleteBatch |
| `TestInvalidator_ConcurrentInvalidation` | Concurrent invalidation events don't race |
| `TestInvalidator_CleanShutdown` | Invalidator stops cleanly on context cancel |

#### `pkg/embedding/openai`

| Test | Description |
|---|---|
| `TestOpenAI_Embed_Success` | Mock HTTP server returns valid embedding → correct vector |
| `TestOpenAI_Embed_RateLimit` | Mock returns 429 → retries with backoff, succeeds on retry |
| `TestOpenAI_Embed_ServerError` | Mock returns 500 → retries, eventually returns error |
| `TestOpenAI_Embed_MalformedJSON` | Mock returns invalid JSON → error |
| `TestOpenAI_Embed_EmptyResponse` | Mock returns empty data array → error |
| `TestOpenAI_EmbedBatch` | Mock handles batch of 3 texts → 3 vectors |
| `TestOpenAI_Embed_ContextCancellation` | Context canceled mid-request → immediate error |
| `TestOpenAI_Embed_CustomBaseURL` | Custom base URL is used correctly |
| `TestOpenAI_APIKeyNotLogged` | Verify API key does not appear in logs (capture slog output) |

#### `pkg/cdc/webhook`

| Test | Description |
|---|---|
| `TestWebhook_ValidEvent` | POST valid JSON → 200 OK, event emitted to channel |
| `TestWebhook_MalformedJSON` | POST invalid JSON → 400 Bad Request |
| `TestWebhook_MissingFields` | POST with missing source_id → 400 Bad Request |
| `TestWebhook_AuthValid` | POST with valid Bearer token → 200 |
| `TestWebhook_AuthInvalid` | POST with wrong Bearer token → 401 Unauthorized |
| `TestWebhook_AuthMissing` | POST with no auth header when auth configured → 401 |
| `TestWebhook_MethodNotAllowed` | GET request → 405 |
| `TestWebhook_Shutdown` | Cancel context → server shuts down gracefully |

#### `pkg/cdc/polling`

| Test | Description |
|---|---|
| `TestPolling_DetectsChange` | Hash function returns new hash → change event emitted |
| `TestPolling_NoChange` | Hash function returns same hash → no event |
| `TestPolling_SourceDeleted` | Hash function returns error (source gone) → deletion event |
| `TestPolling_Interval` | Verify polling respects configured interval (within 10% tolerance) |
| `TestPolling_Shutdown` | Cancel context → poller stops |

#### `pkg/server/http`

| Test | Description |
|---|---|
| `TestHTTP_Lookup_Hit` | POST /v1/lookup with cached prompt → 200 with hit=true |
| `TestHTTP_Lookup_Miss` | POST /v1/lookup with uncached prompt → 200 with hit=false |
| `TestHTTP_Lookup_BadJSON` | POST /v1/lookup with invalid body → 400 |
| `TestHTTP_Lookup_MissingFields` | POST /v1/lookup without namespace → 400 |
| `TestHTTP_Store_Success` | POST /v1/store → 201 with entry ID |
| `TestHTTP_Store_BadJSON` | POST /v1/store with invalid body → 400 |
| `TestHTTP_Invalidate_Success` | POST /v1/invalidate → 200 with count |
| `TestHTTP_DeleteEntry_Success` | DELETE /v1/entries/{id} → 204 |
| `TestHTTP_DeleteEntry_NotFound` | DELETE /v1/entries/{id} → 204 (idempotent) |
| `TestHTTP_Stats` | GET /v1/stats → 200 with stats JSON |
| `TestHTTP_Healthz` | GET /healthz → 200 |
| `TestHTTP_NotFound` | GET /nonexistent → 404 |
| `TestHTTP_ContentType` | All JSON responses have Content-Type: application/json |

#### `pkg/server/grpc`

| Test | Description |
|---|---|
| `TestGRPC_Lookup_Hit` | Lookup RPC with cached prompt → hit response |
| `TestGRPC_Lookup_Miss` | Lookup RPC with uncached prompt → miss response |
| `TestGRPC_Store_Success` | Store RPC → returns entry ID |
| `TestGRPC_Invalidate` | Invalidate RPC → returns count |
| `TestGRPC_DeleteEntry` | DeleteEntry RPC → success |
| `TestGRPC_Stats` | GetStats RPC → returns stats |

All gRPC tests use `bufconn` (in-memory gRPC connection) — no actual ports opened.

#### `pkg/reverb` (Client facade)

| Test | Description |
|---|---|
| `TestClient_LookupExactHit` | Store then lookup identical prompt → exact hit |
| `TestClient_LookupSemanticHit` | Store then lookup similar prompt → semantic hit |
| `TestClient_LookupMiss` | Lookup unrelated prompt → miss |
| `TestClient_Store_WritesToBothTiers` | After Store, both exact and semantic lookups work |
| `TestClient_Invalidate_RemovesFromBothTiers` | After Invalidate, both tiers return miss |
| `TestClient_TTLExpiry` | Store with short TTL, advance fake clock → miss |
| `TestClient_NamespaceIsolation` | Store in ns-A, lookup in ns-B → miss |
| `TestClient_ModelIDIsolation` | Store with model-A, lookup with model-B → miss (when scope_by_model=true) |
| `TestClient_EmbeddingFailure_Degradation` | FailingProvider → exact tier still works, semantic returns miss |
| `TestClient_StoreWithEmbeddingFailure` | FailingProvider → entry stored in exact tier, EmbeddingMissing=true |
| `TestClient_Stats` | Store 5 entries, verify stats reflect them |
| `TestClient_Close_StopsGoroutines` | Close() returns, no goroutine leaks (use goleak) |
| `TestClient_ConcurrentLookupAndStore` | 50 goroutines doing concurrent Lookup/Store → no races |

All Client tests use `memory.Store`, `flat.Index`, and `fake.Provider` — no network, no disk.

### 17.4. Integration tests

Integration tests live in `test/integration/` and are tagged `//go:build integration`. They test the full system end-to-end with real infrastructure (Redis, NATS, HTTP endpoints).

| Test file | Scenarios |
|---|---|
| `client_test.go` | Full Client lifecycle: store → exact hit → semantic hit → invalidate → miss. Uses BadgerDB + HNSW + fake embeddings. |
| `http_test.go` | Spin up HTTP server, exercise all endpoints via `net/http` client. |
| `grpc_test.go` | Spin up gRPC server, exercise all RPCs via generated client. |
| `cdc_webhook_test.go` | Store entry, POST webhook event, verify entry invalidated within 2s. |
| `redis_test.go` | Full conformance suite + Client lifecycle using Redis store. Requires `REVERB_TEST_REDIS_ADDR`. |
| `nats_test.go` | CDC via NATS: publish change event, verify invalidation. Requires `REVERB_TEST_NATS_URL`. |

### 17.5. Benchmarks

```go
func BenchmarkLookupExactHit(b *testing.B)       { ... } // target: < 0.5ms
func BenchmarkLookupSemanticHit(b *testing.B)    { ... } // target: < 50ms
func BenchmarkLookupMiss(b *testing.B)           { ... }
func BenchmarkStore(b *testing.B)                { ... }
func BenchmarkInvalidation100Entries(b *testing.B) { ... }
func BenchmarkNormalize(b *testing.B)            { ... }
func BenchmarkSHA256Hash(b *testing.B)           { ... }
func BenchmarkVectorSearchFlat10K(b *testing.B)  { ... }
func BenchmarkVectorSearchFlat50K(b *testing.B)  { ... }
func BenchmarkVectorSearchHNSW100K(b *testing.B) { ... }
func BenchmarkVectorSearchHNSW1M(b *testing.B)   { ... }
func BenchmarkStoreMemoryPut(b *testing.B)       { ... }
func BenchmarkStoreBadgerPut(b *testing.B)       { ... }
```

---

## 18. Container-based test infrastructure

All tests — unit, integration, and benchmarks — run in containers with no host-level dependencies beyond Docker.

### 18.1. docker-compose.yml for test infrastructure

```yaml
# test/docker-compose.yml

version: "3.9"

services:
  redis:
    image: redis:7-alpine
    ports:
      - "6399:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 2s
      timeout: 3s
      retries: 5

  nats:
    image: nats:2-alpine
    command: ["-js"]
    ports:
      - "4322:4222"
    healthcheck:
      test: ["CMD", "sh", "-c", "wget -qO- http://localhost:8222/healthz || exit 1"]
      interval: 2s
      timeout: 3s
      retries: 5

  test-runner:
    build:
      context: ..
      dockerfile: Dockerfile.test
    depends_on:
      redis:
        condition: service_healthy
      nats:
        condition: service_healthy
    environment:
      REVERB_TEST_REDIS_ADDR: "redis:6379"
      REVERB_TEST_NATS_URL: "nats://nats:4222"
      CGO_ENABLED: "0"
    volumes:
      - ../:/app
    working_dir: /app
    command: >
      sh -c "
        echo '=== Unit tests ===' &&
        go test -race -count=1 -timeout 120s ./pkg/... ./internal/... &&
        echo '=== Integration tests ===' &&
        go test -race -tags integration -count=1 -timeout 300s ./test/integration/... &&
        echo '=== Benchmarks ===' &&
        go test -bench=. -benchmem -benchtime=3s -run='^$' ./...
      "
```

### 18.2. Dockerfile.test

```dockerfile
# Dockerfile.test — test image with all build and test tools

FROM golang:1.22-alpine

RUN apk --no-cache add git gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Pre-compile test binaries to speed up repeated runs
RUN go test -race -count=0 ./...
```

### 18.3. Makefile targets

```makefile
.PHONY: build test test-unit test-integration test-all lint proto bench docker docker-test clean

# --- Build ---
build:
	go build -o bin/reverb ./cmd/reverb

# --- Unit tests (no external deps, runs locally) ---
test-unit:
	go test -race -count=1 -timeout 120s ./pkg/... ./internal/...

# --- Integration tests (requires docker-compose infra) ---
test-integration:
	cd test && docker compose up -d redis nats --wait
	REVERB_TEST_REDIS_ADDR=localhost:6399 \
	REVERB_TEST_NATS_URL=nats://localhost:4322 \
	go test -race -tags integration -count=1 -timeout 300s ./test/integration/...

# --- All tests inside containers (zero host deps beyond Docker) ---
test-all:
	cd test && docker compose up --build --abort-on-container-exit test-runner

# --- Convenience alias ---
test: test-unit

# --- Linting ---
lint:
	golangci-lint run ./...

# --- Protobuf codegen ---
proto:
	protoc --go_out=. --go-grpc_out=. pkg/server/proto/reverb.proto

# --- Benchmarks ---
bench:
	go test -bench=. -benchmem -benchtime=3s -run='^$$' ./...

# --- Production Docker image ---
docker:
	docker build -t reverb:latest .

# --- Full containerized test (same as test-all) ---
docker-test: test-all

# --- Coverage ---
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./pkg/... ./internal/...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# --- Cleanup ---
clean:
	cd test && docker compose down -v
	rm -rf bin/ coverage.out coverage.html data/
```

### 18.4. CI pipeline (GitHub Actions)

```yaml
# .github/workflows/ci.yml

name: CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - name: Unit tests
        run: make test-unit
      - name: Lint
        run: |
          go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
          make lint
      - name: Coverage
        run: make coverage
      - uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: coverage.html

  integration:
    runs-on: ubuntu-latest
    needs: unit
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - name: Integration tests
        run: make test-all

  bench:
    runs-on: ubuntu-latest
    needs: unit
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - name: Benchmarks
        run: make bench
```

---

## 19. Dependencies

All third-party dependencies, selected for stability and minimal footprint.

| Dependency | Purpose | Version |
|---|---|---|
| `github.com/dgraph-io/badger/v4` | Embedded key-value store | v4.x |
| `github.com/redis/go-redis/v9` | Redis client | v9.x |
| `github.com/viterin/vek` | SIMD-accelerated vector operations (dot product, norm) | latest |
| `github.com/google/uuid` | UUIDv7 generation | v1.6+ |
| `github.com/nats-io/nats.go` | NATS client (optional, for CDC) | v1.x |
| `github.com/prometheus/client_golang` | Prometheus metrics | v1.x |
| `go.opentelemetry.io/otel` | OpenTelemetry tracing | v1.x |
| `google.golang.org/grpc` | gRPC server and codegen | v1.x |
| `google.golang.org/protobuf` | Protobuf serialization | v1.x |
| `golang.org/x/text` | Unicode normalization (NFC) | latest |
| `gopkg.in/yaml.v3` | YAML config parsing | v3 |
| `github.com/stretchr/testify` | Test assertions (dev only) | v1.9+ |
| `go.uber.org/goleak` | Goroutine leak detection in tests (dev only) | v1.x |

### 19.1. Build constraints

- The `nats` CDC listener is behind a build tag: `//go:build nats`. This avoids pulling in the NATS dependency for users who don't need it.
- The `redis` store is behind a build tag: `//go:build redis`.
- The core library (memory store, flat vector index, webhook CDC, OpenAI embedding) has minimal dependencies.

---

## 20. Production Dockerfile

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags "nats redis" -o /reverb ./cmd/reverb

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
COPY --from=builder /reverb /usr/local/bin/reverb
EXPOSE 8080 9090 9091 9100
ENTRYPOINT ["reverb"]
CMD ["--config", "/etc/reverb/reverb.yaml"]
```

---

## 21. Implementation order

Recommended build sequence, designed so each phase produces a testable, runnable artifact.

### Phase 1 — Foundation (week 1–2)

1. `internal/hashutil` — SHA-256 helpers + tests
2. `internal/retry` — retry with backoff + tests
3. `internal/testutil` — FakeClock, EntryBuilder, embedding helpers
4. `pkg/normalize` — prompt normalization + tests
5. `pkg/store/store.go` — Store interface definition
6. `pkg/store/conformance` — conformance test suite
7. `pkg/store/memory` — in-memory store + conformance tests
8. `pkg/vector/index.go` — VectorIndex interface definition
9. `pkg/vector/conformance` — vector conformance test suite
10. `pkg/vector/flat` — flat brute-force index + conformance tests
11. `pkg/embedding/provider.go` — EmbeddingProvider interface
12. `pkg/embedding/fake` — fake + failing provider
13. `pkg/embedding/openai` — OpenAI implementation + HTTP mock tests

**Milestone**: Can embed text and search a flat vector index in memory. All test doubles available.

### Phase 2 — Core cache (week 2–3)

14. `pkg/cache/exact` — exact-match cache tier + tests (using FakeClock)
15. `pkg/cache/semantic` — semantic cache tier + tests (using fake.Provider)
16. `pkg/reverb/config.go` — configuration types + validation tests
17. `pkg/reverb/client.go` — Client facade with Lookup + Store + tests
18. `pkg/lineage/index.go` — lineage index + tests
19. `pkg/lineage/invalidator.go` — invalidation engine + tests

**Milestone**: Full Lookup → Store → Invalidate cycle works in-memory. All unit tests pass.

### Phase 3 — Persistence and production readiness (week 3–4)

20. `pkg/store/badger` — BadgerDB store + conformance tests
21. `pkg/vector/hnsw` — HNSW vector index + conformance tests
22. `pkg/metrics/metrics.go` — Prometheus metrics
23. `pkg/metrics/tracing.go` — OpenTelemetry spans
24. Background goroutines: expiry reaper, metrics updater

**Milestone**: Durable, observable cache suitable for production use as a library.

### Phase 4 — Server mode (week 4–5)

25. `pkg/server/proto/reverb.proto` — protobuf definitions + codegen
26. `pkg/server/grpc.go` — gRPC service + bufconn tests
27. `pkg/server/http.go` — HTTP/REST handlers + httptest tests
28. `cmd/reverb/main.go` — standalone server binary
29. Dockerfile + Dockerfile.test

**Milestone**: Reverb runs as a standalone microservice.

### Phase 5 — CDC and extended backends (week 5–6)

30. `pkg/cdc/webhook` — webhook CDC listener + tests
31. `pkg/cdc/polling` — polling CDC listener + tests
32. `pkg/cdc/nats` — NATS CDC listener + tests
33. `pkg/store/redis` — Redis store + conformance tests
34. `pkg/embedding/ollama` — Ollama embedding provider + tests

**Milestone**: Full feature set. Ready for production deployment.

### Phase 6 — Polish (week 6)

35. `test/docker-compose.yml` — test infrastructure
36. `test/integration/` — full integration test suite
37. Benchmark suite
38. Makefile with all targets
39. `.github/workflows/ci.yml` — CI pipeline
40. README with quickstart, architecture diagram, and API reference

---

## 22. Open design decisions

These are decisions that should be resolved during implementation but have reasonable defaults.

| Decision | Default | Alternatives | Notes |
|---|---|---|---|
| Hash function for prompts | SHA-256 | xxHash (faster, non-crypto) | SHA-256 is safe and fast enough. xxHash if profiling shows hashing is a bottleneck. |
| Embedding dimension normalization | L2-normalize before storage | Store raw | Normalized vectors allow cosine similarity via dot product (faster). |
| Max cache size / eviction | No max; rely on TTL + invalidation | LRU eviction at configurable max entries | For the MVP, TTL is sufficient. LRU can be added if users report unbounded growth. |
| Protobuf vs JSON for BadgerDB serialization | Protobuf (smaller, faster) | JSON (human-readable) | Protobuf for production; add a debug JSON mode if needed. |
| HNSW library | Custom implementation using `vek` for SIMD | `github.com/coder/hnsw`, `github.com/qdrant/hnswlib` via CGO | Pure Go is preferred for portability. Fall back to CGO-based hnswlib if recall is insufficient. |

---

## 23. Security considerations

- **API keys**: The embedding provider API key must never be logged or returned in API responses. It is stored in-memory only and can be provided via environment variable.
- **Prompt/response data**: Cache entries contain the full prompt and response. If the application handles sensitive data (PII, PHI), the operator must configure encryption-at-rest (BadgerDB supports this natively; Redis requires TLS + encrypted storage).
- **Webhook authentication**: The CDC webhook endpoint should be secured with a shared secret (via `Authorization: Bearer <token>` header). The webhook listener validates the token before processing events.
- **Namespace isolation**: Namespaces are a logical partition, not a security boundary. If hard multi-tenancy is required, deploy separate Reverb instances.

---

## 24. Design review checklist

The following checklist verifies this document is complete and implementation-ready.

- [x] **Problem clearly stated** — Section 1 defines the gap and goals.
- [x] **All interfaces defined** — Sections 6.1–6.4 define every pluggable boundary with Go code.
- [x] **All data types defined** — Section 4.1 defines `CacheEntry` and `SourceRef` with all fields.
- [x] **Core algorithms specified** — Sections 7 (lookup), 8 (invalidation), 9 (normalization) provide step-by-step logic.
- [x] **Package structure defined** — Section 5 lists every file including test files.
- [x] **Configuration complete** — Section 10 defines all config fields with types, defaults, and env vars.
- [x] **Public API documented** — Section 11 shows the Go facade; Section 12 shows the HTTP API; Section 13 shows gRPC.
- [x] **Error handling specified** — Section 16 covers every failure mode with recovery behavior.
- [x] **Concurrency model documented** — Section 15 specifies locking and goroutine lifecycle.
- [x] **Test doubles defined** — Section 17.1 provides complete code for fake embedder, failing embedder, fake clock, and entry builder.
- [x] **Conformance suites defined** — Section 17.2 provides complete code for Store and VectorIndex conformance suites.
- [x] **Unit tests exhaustively listed** — Section 17.3 lists every test scenario for every package with descriptions.
- [x] **Integration tests defined** — Section 17.4 lists all integration test files and scenarios.
- [x] **Benchmarks defined** — Section 17.5 lists all benchmark functions with target latencies.
- [x] **Container test infrastructure defined** — Section 18 provides docker-compose.yml, Dockerfile.test, Makefile targets, and CI pipeline.
- [x] **Dependencies listed** — Section 19 lists all third-party packages with versions.
- [x] **Build/deploy artifacts defined** — Sections 18.3, 20 provide Makefile and Dockerfiles.
- [x] **Implementation order specified** — Section 21 breaks the work into 6 phases with numbered steps and milestones.
- [x] **Security considerations addressed** — Section 23 covers key management, data sensitivity, and webhook auth.
- [x] **Open decisions documented** — Section 22 lists unresolved choices with defaults.
- [x] **No ambiguous requirements** — Every "should" has been resolved to a concrete default or explicit open decision.
- [x] **All tests runnable in containers** — `make test-all` runs everything inside Docker with zero host dependencies.
- [x] **Implementable by Claude Code** — All interfaces, algorithms, types, test doubles, and file paths are specified to write code directly from this document.
