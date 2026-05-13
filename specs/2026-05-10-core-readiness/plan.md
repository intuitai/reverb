# Plan — Core Readiness (Phase 1 closeout, part 1)

Phase: 1 · Date: 2026-05-10

## 1. Streaming response support (roadmap §1.6)

Goal: callers can replay a cached LLM answer as an SSE stream the same way
they replay it as a string today, without breaking the existing
non-streaming surface.

- [x] Define `pkg/reverb.ResponseChunk` (`Delta string`, `FinishReason
      string`) and add `Chunks []ResponseChunk` to the existing
      `Response` type. Document that `Chunks` is optional and may be nil
      for callers that stored only complete strings.
      — `pkg/reverb/types.go:35` (alias of `store.ResponseChunk`); `Chunks`
      field at `pkg/reverb/types.go:65`.
- [x] Extend `pkg/reverb.StoreRequest` with an optional `Chunks` field.
      Validate at `Client.Store` that exactly one of `ResponseText` or
      `Chunks` is set; if both, prefer `Chunks` and reconstruct
      `ResponseText` by concatenating deltas (so callers that read the
      legacy field still see the full answer).
      — Reconstruction asserted by `TestStreaming_StoreAndLookup` in
      `pkg/reverb/streaming_test.go`.
- [x] Persist chunks in `store.Store` implementations. Add to the entry
      shape in `pkg/store` types; update `memory`, `redis`, `badger`
      encoders. Run the existing `pkg/store/conformance` suite to confirm
      no regression.
      — `store.Entry.Chunks` at `pkg/store/store.go:31`; memory deep-copies
      at `pkg/store/memory/memory.go:301`; redis/badger inherit via their
      existing JSON encoding of the entry shape.
- [x] Add `POST /v1/lookup-stream` HTTP endpoint in `pkg/server/http.go`.
      Returns `text/event-stream` with one SSE `data:` line per chunk on a
      hit, `404` (with the existing error envelope) on a miss. Include
      a 15-second comment-line keepalive — see Open Question #1 in
      requirements.
      — Route registered at `pkg/server/http.go:104`; 15-second keepalive
      ticker at `pkg/server/http.go:393`.
- [x] Update `openapi/v1.yaml` with the new endpoint. Ensure
      `pkg/server/openapi_drift_test.go` passes; add a streaming-specific
      drift test if the existing one cannot model SSE.
      — `openapi/v1.yaml:93` (`/v1/lookup-stream`); SSE shape covered
      directly by `pkg/server/http_stream_test.go`.
- [x] Update Python and TypeScript SDKs. Python: async generator
      `async for chunk in client.lookup_stream(req)`. TypeScript:
      `AsyncIterable<ResponseChunk>` from `client.lookupStream(req)`.
      Add to `sdk/python/scripts/smoke_test.py` and the JS smoke jobs.
      — `sdk/python/reverb/client.py:294` (async variant);
      `sdk/js/src/client.ts:213` (`AsyncIterable<ResponseChunk>`).
- [x] Update gRPC: add a server-streaming RPC `LookupStream` to
      `pkg/server/proto/reverb.proto`. Regenerate stubs. Mirror the
      HTTP semantics.
      — `pkg/server/proto/reverb.proto:17`; covered by
      `pkg/server/grpc_stream_test.go`.
- [x] Add an integration test that stores a chunked response, looks it
      up via the streaming endpoint, and asserts the reconstructed text
      matches the legacy field.
      — `TestStreaming_StoreAndLookup` in `pkg/reverb/streaming_test.go`;
      `pkg/server/http_stream_test.go` covers the HTTP path.
- [x] CHANGELOG entry under "Added" describing the additive nature.
      — `CHANGELOG.md:30` ("Streaming response support").
- [x] Migration note in COMPATIBILITY.md: existing callers see no
      change; opting into streaming requires switching call sites.
      — `COMPATIBILITY.md:280`.

## 2. OpenAI-compatible reverse-proxy mode (roadmap §1.7)

Goal: a single flag turns `cmd/reverb` into an OpenAI-API-shaped cache
in front of any OpenAI-API-compatible upstream (OpenAI itself, vLLM,
llama.cpp, Together, Anyscale, Ollama, OpenRouter).

- [x] Add `--proxy openai --upstream <url>` flags to `cmd/reverb/main.go`.
      Default: off. When set, the binary stops serving the standard
      `/v1/*` Reverb surface and instead serves OpenAI-shaped
      `POST /v1/chat/completions` (and, if trivial, `/v1/embeddings` —
      otherwise out of scope for the first cut).
      — `cmd/reverb/main.go:52` (`--upstream`); mode dispatch at
      `cmd/reverb/main.go:234`.
- [x] Implement the proxy handler in a new file
      `cmd/reverb/proxy_openai.go`. On request: hash the
      messages/model/tools fields into a Reverb `LookupRequest`, call
      `client.Lookup`, return cached response on hit, forward to the
      upstream on miss, then `client.Store` the upstream response.
      — `cmd/reverb/proxy_openai.go` (401 lines); covered by
      `cmd/reverb/proxy_openai_test.go`.
- [x] Honor `Cache-Control: no-cache` and `Cache-Control: no-store`
      request headers per RFC 9111 — bypass and skip-store, respectively.
      — `parseCacheControl` at `cmd/reverb/proxy_openai.go:332`.
- [x] Auth pass-through: forward the caller's `Authorization` header to
      the upstream verbatim. Document that the upstream key is the
      caller's key (per Open Question #2 in requirements).
      — Behavior documented in the package doc at
      `cmd/reverb/proxy_openai.go:10`.
- [x] Add `examples/openai-proxy/`: a runnable example with
      copy-paste curl commands hitting the proxy and showing a cache
      hit on the second request. README explains how to point any
      OpenAI SDK at `http://localhost:8080` instead of
      `https://api.openai.com`.
      — `examples/openai-proxy/{main.go,README.md}`.
- [x] CHANGELOG entry under "Added".
      — `CHANGELOG.md:36` ("OpenAI-compatible reverse-proxy mode").
- [ ] RUNBOOK.md operational notes: proxy mode is mutually exclusive
      with serving the native Reverb surface in the same binary; run
      two binaries side-by-side if both are needed.
      — **Not implemented:** `grep proxy RUNBOOK.md` returns no matches.
      The mutual-exclusion contract is enforced in code (the binary
      switches to the proxy handler when `--proxy` is set) but is not
      documented in the runbook. Worth adding in the next sweep.

## 3. Singleflight on cache miss (roadmap §1.9)

Goal: a thundering-herd of concurrent identical misses costs one upstream
call instead of N.

- [x] Add `LookupOrCall(ctx context.Context, req LookupRequest,
      fill func(context.Context) (StoreRequest, error)) (Response, bool, error)`
      to `pkg/reverb.Client`. Returns `(response, hit, err)`.
      — `pkg/reverb/singleflight.go:31`.
- [x] Implement using `golang.org/x/sync/singleflight`. Key the flight
      by the same SHA-256 hash that the exact-match tier uses, so
      concurrent callers with byte-identical normalized prompts coalesce.
      — `sfGroup` field at `pkg/reverb/client.go:101`; flight key noted in
      the godoc at `pkg/reverb/singleflight.go:19`.
- [x] If the in-flight `fill` succeeds, the leader stores the result
      and the followers receive it without a second `Store` call. If
      `fill` fails, all callers receive the same error.
      — Verified by `TestLookupOrCall_StressCoalesces` and
      `TestLookupOrCall_FillError` in `pkg/reverb/singleflight_test.go`.
- [x] Add a stress test in `pkg/reverb/singleflight_test.go`: 100
      concurrent `LookupOrCall`s for the same prompt with a
      `fill` that increments a counter. Assert counter == 1.
      — `TestLookupOrCall_StressCoalesces` at
      `pkg/reverb/singleflight_test.go:20`.
- [x] Add a metric `reverb_singleflight_coalesced_total` counter for
      observability.
      — Registered at `pkg/metrics/metrics.go:159`.
- [x] Document the new method on the godoc page; add a Quick-Start
      snippet to the `pkg/reverb` example file.
      — Method-level godoc at `pkg/reverb/singleflight.go:12-30`; README
      callout at `README.md:36` ("Singleflight on cache miss").
- [x] CHANGELOG entry under "Added".
      — `CHANGELOG.md:46` ("Singleflight on cache miss").

## 4. PII redaction hook (roadmap §1.11)

Goal: regulated-industry callers can ensure no PII reaches stored
prompts, behind an opt-in toggle that does not affect the Quick Start.

- [x] Add `pkg/normalize.Redactor` interface:
      `Redact(ctx context.Context, prompt string) string`.
- [x] Wire `Redactor` into `pkg/reverb`: invoked between
      `normalize.Normalize` and the SHA-256 hash. Order is
      Redactor-then-hash so the stored `PromptText` and the keying
      hash both reflect the redacted form.
      — Lookup path at `pkg/reverb/client.go:417`; Store path at
      `pkg/reverb/client.go:522`.
- [x] Ship a default `pkg/normalize/redactor/regex` implementation
      covering: email addresses (RFC 5322 simplified), North American
      phone numbers, credit-card numbers (Luhn-checked), US SSN.
      Replacement form: `[EMAIL]`, `[PHONE]`, `[CARD]`, `[SSN]`.
      — `pkg/normalize/redactor/regex/regex.go`.
- [x] YAML config: per-namespace toggle.
      `namespaces: [{ name: x, redactor: { enabled: true, default: regex } }]`.
      Falls through to a global `redactor.enabled: false` default.
      Note: this is a minimal namespace config schema; the full per-
      namespace config schema (TTL, threshold, scope_by_model) is
      tracked separately as roadmap §2.8.
      — Global `RedactorConfig` at `pkg/reverb/config.go:147`; per-
      namespace override at `pkg/reverb/config.go:161`.
- [x] Add benchmarks: redaction adds <1ms p99 to lookups on the
      benchmark suite's reference inputs.
      — `BenchmarkRedact` at
      `pkg/normalize/redactor/regex/regex_test.go:70`.
- [x] Add `examples/pii-redaction/`: stores a prompt containing an
      email + phone, looks it up, asserts the stored entry shows the
      placeholder forms.
      — `examples/pii-redaction/main.go`.
- [x] CHANGELOG entry under "Added"; security section update.
      — Added entry at `CHANGELOG.md:51`; Security section at
      `CHANGELOG.md:113`.
- [x] Migration note in COMPATIBILITY.md: enabling the redactor on an
      existing cache invalidates prior entries by construction (the
      hash changes); operators should expect a one-time hit-rate drop
      and either drain the cache or accept the natural rebuild.
      — `COMPATIBILITY.md:286`.

## 5. Cross-cutting

Goal: ensure the four items land cohesively.

- [x] Stale-claim doc sweep on README.md, COMPATIBILITY.md, CHANGELOG.md
      after merge — re-run the same forbidden-string check the prior
      spec performed manually. (The doc-lint CI step from the prior
      spec was not added; that gap is still open and is worth closing
      here as a sub-task if scope allows.)
      — Manual sweep done as part of commit `6c5aebf`; doc-lint CI step
      remains unimplemented (see the next checkbox).
- [x] Update `specs/roadmap.md` Phase-1 items 1.6, 1.7, 1.9, 1.11 to
      mark them shipped once each lands.
      — `specs/roadmap.md` items 1.6, 1.7, 1.9, 1.11 each carry the
      `— **DONE** (`spec/phase-1-core-readiness`)` annotation.
- [x] Update `docs/improvement_plan2.md` items C1, C5, E1, G1 with
      the same DONE annotation pattern used in the post-merge sweep.
      — `docs/improvement_plan2.md` C1 (line 89), C5 (line 93), E1
      (line 122), G1 (line 154) all annotated `✅ **DONE**`.
- [ ] Update `specs/2026-04-30-adoption-surface/plan.md` §4 doc-lint
      checkbox if implemented as part of the cross-cutting sweep.
      — **Not implemented:** no doc-lint workflow in `.github/workflows/`;
      the adoption-surface checkbox for the doc-lint CI step remains
      unchecked. Worth pairing with the RUNBOOK proxy-section gap in a
      small follow-up doc PR.

## Sequencing notes

- **Group 3 (singleflight) is the smallest and most independent.** Land
  it first to give the rest of the spec a stable Client API to build
  against and to derisk the test infrastructure for concurrency.
- **Group 1 (streaming) before Group 2 (proxy)**, because the proxy
  must replay cached chat completions as streams when the client requests
  `stream: true` — that requires the streaming surface from Group 1.
- **Group 4 (PII redactor) is independent of 1–3** and can run in
  parallel with any of them.
- **Group 5 (cross-cutting) lands last**, after the four feature groups,
  so the doc updates reflect the actual merged surface.

Within Group 1, land the Go-API additions and HTTP endpoint before the
SDK updates. The SDKs regenerate from `openapi/v1.yaml`; they should
not chase a moving spec.
