# Benchmarks & Quality Budgets

This document publishes Reverb's reference latency baselines and the quality
budgets the cache is required to meet on every change. Both are enforced
continuously: the false-positive budget is a hard gate in CI (the build fails
if it is exceeded); latency baselines are tracked via the scheduled
**Benchmarks** workflow described below.

If you are evaluating Reverb for adoption, the numbers here let you reason
about whether the cache fits your latency tail and whether its
quality-correctness story is real or aspirational.

## Reference environment

The numbers below were collected with:

| Field | Value |
|---|---|
| Hardware | Apple M4 (10 cores) |
| OS | Darwin 24.6.0 |
| Go toolchain | 1.26.0 |
| Embedding dimensions | 128 |
| Vector index | `flat` (brute-force O(n)) |
| Persistence store | `memory` |
| Embedding provider | `ControlledProvider` (deterministic, see `benchmark/controlled_provider.go`) |
| Similarity threshold | 0.95 |
| `benchtime` | 2s per benchmark |

Numbers will scale roughly linearly with single-core performance on other
hardware. The shape of the table — flat exact-tier, linear semantic/miss tier
in N — is a property of the algorithms, not the hardware, and should
reproduce anywhere.

## Latency baselines

### Single-shot lookup at N=100 entries

| Path | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Exact hit (`Lookup` → tier=exact) | 3,323 | 2,906 | 30 |
| Semantic hit (`Lookup` → tier=semantic) | 10,628 | 3,312 | 39 |
| Miss (`Lookup` → tier=miss) | 14,586 | 3,345 | 37 |
| `Store` | 7,791 | 4,817 | 48 |

### Lookup latency vs. index size

How lookup latency scales with the number of cached entries. The exact tier
is hash-keyed and is independent of N; the semantic and miss tiers scan the
flat vector index and are linear in N.

| Path | N=100 | N=1,000 | N=10,000 |
|---|---:|---:|---:|
| **Exact hit** | 3.4 µs | 3.3 µs | 3.4 µs |
| **Semantic hit** | 10.9 µs | 88.3 µs | 866 µs |
| **Miss** | 14.6 µs | 91.6 µs | 862 µs |

Reading the table:

- The exact tier is the fast path. If your workload has a heavy long tail of
  identical prompts (chatbots, FAQ-style assistants), the cache pays for
  itself at sub-10 µs per hit even at 10K entries.
- The semantic tier with the `flat` index is appropriate up to roughly
  50K entries. Beyond that, switch to the `hnsw` index (`vector.backend:
  "hnsw"` in the operator config) — its log-N behaviour is the reason the
  package exists.
- Miss latency tracks semantic-hit latency closely because both paths execute
  the full vector scan; the miss path also pays the embedding cost without
  amortizing it via a downstream LLM call.

## Quality budgets

These are the published, enforced budgets. Each one is asserted by a test in
the `benchmark` package and the build fails if any budget is exceeded.

### Published false-positive budget

> **No unrelated query may produce a cache hit.** Over the
> `UnrelatedPairs` evaluation set (10 stored / unrelated query pairs at
> threshold 0.95), the false-positive count must be **zero**.

- **Current measurement:** 0 / 10 (0.0%)
- **Enforced by:** `TestEval_FalsePositiveRate` in
  [`benchmark/eval_falsepositive_test.go`](benchmark/eval_falsepositive_test.go)
- **Failure mode:** any unrelated query returning `Hit=true` fails the test
  with the offending stored prompt, query, similarity score, and tier.

This is the budget that defines the cache as a *cache* rather than a fuzzy
search engine: a well-tuned threshold should produce zero hits on
domain-distinct prompt pairs. We report it as a hard zero rather than an
"≤ 5%" tolerance because the controlled embedding provider produces
deterministic similarities, so the test is reproducible and any non-zero
result is a real defect, not noise.

### Supporting quality assertions

These are not headline budgets but they ride on the same threshold and
provide the dose-response coverage that makes the headline number
trustworthy.

| Assertion | Current | Enforced by |
|---|---:|---|
| Near-threshold negatives (sim=0.94, threshold=0.95) | 0 / 16 hits | `TestEval_FalsePositiveRate_NearThreshold` |
| Paraphrase precision (sim=0.97, threshold=0.95) | 16 / 16 (100%) | `TestEval_ParaphraseHitPrecision` |
| Per-category paraphrase precision (5 categories) | 100% each | `TestEval_ParaphraseHitPrecision_ByCategory` |
| Threshold-sweep monotonicity (≥ similarity ⇒ hit; > similarity ⇒ miss) | passes at 5 thresholds | `TestEval_ParaphraseThresholdSweep` |
| Invalidation correctness (delete, content-hash change, idempotency, multi-source, no-false-invalidation) | 5 / 5 | `TestEval_Invalidation*` |

The threshold sweep is the most informative of these: it pins the
boundary behaviour of the semantic tier (hits at thresholds ≤ pair
similarity, misses above) at five points across the range, which is what
gives the FP-budget number its meaning. Without that, "0 false positives"
would only describe the exact set of pairs in `UnrelatedPairs.var`.

## How to reproduce

```bash
# Quality evals + latency benchmarks (~30s)
make bench-quality

# Just the quality evals (false-positive budget, paraphrase, invalidation)
go test -v -count=1 -run '^TestEval_' ./benchmark/...

# Just the published latency tables
go test -bench='BenchmarkLookup_(ExactHit|SemanticHit|Miss)(_ScaledIndex)?$' \
        -benchmem -benchtime=2s -run='^$' ./benchmark/...
```

The evaluation dataset lives in
[`benchmark/dataset.go`](benchmark/dataset.go) — `Paraphrases` for
positive pairs grouped by linguistic transform, `UnrelatedPairs` for
negatives. To add or change cases, edit that file; the tests pick up the
new entries automatically.

## Regression workflow

The `.github/workflows/benchmarks.yml` workflow runs:

- **On every pull request** that touches `benchmark/`, `pkg/cache/`,
  `pkg/normalize/`, `pkg/lineage/`, or `go.mod` — to fail-fast when a
  change violates a quality budget before review.
- **Weekly on a schedule** (Mondays at 12:00 UTC) — to catch slow drift
  caused by upstream dependency updates.
- **On manual dispatch** (`workflow_dispatch`) — for ad-hoc runs.

The job:

1. Runs the eval test suite (`make bench-quality`'s first half). The
   published false-positive budget and the supporting assertions are
   hard gates here.
2. Runs the latency benchmarks (`make bench-quality`'s second half) and
   uploads the output as a build artifact for trend tracking.

Comparing two artifacts with `benchstat` is the recommended workflow
when investigating a suspected regression:

```bash
benchstat baseline.txt new.txt
```

## Why these and not others

The benchmark suite intentionally does **not** publish:

- **End-to-end LLM latency** — that is dominated by the LLM provider and
  has nothing to do with cache quality.
- **Real-embedder hit rates** — these depend on which model you pick and
  what your prompts look like, and would be misleading as a vendor claim.
  The threshold-sweep test is the substitute: it verifies *the cache
  responds correctly* to whatever similarities its embedder produces.
- **Throughput numbers** — the cache itself is not the bottleneck in
  any realistic LLM pipeline; the lookup-latency table above is the
  number that matters for the tail of a request.
