# lodestar — architecture

Ratified 2026-06-30. The design decision behind it: **build our own, native.** Not because the existing tools (graphify, glia, logiclens, GitNexus) are bad — they're good, and four of them independently converged on the same shape — but because (a) the valuable layer (cross-service contracts) isn't in any single one, so we build it regardless; (b) one native framework for structure *and* contracts beats a Python product plus a native bolt-on; (c) precise entity kinds for our resolvers, with no lossy transform; and (d) this is a tool you lean on for your own deepest problem — own it. We *study* their convergent pattern and reimplement the small pure pieces (the key-normalizers); we run none of their products.

## The pipeline

```
repos ──▶ PARSE ──▶ CONTRACTS ──▶ RESOLVE ──▶ EMIT
         (struct)   (per-proto)   (key-join)   (graph + report)
```

### 1. Parse — `go-tree-sitter`, config-driven per language
Per-language `LanguageConfig` (the graphify pattern, native Go): which tree-sitter node types are classes / functions / methods / imports / calls. Emits structural **nodes** (file, module, class, function, method, interface) + **edges** (`contains`, `imports`, `calls`, `inherits`, `implements`). Deterministic — no LLM. Deep cross-file call resolution (function A → function B across files) is a later enhancement; the structural + contract layers don't need it.

### 2. Contracts — per-protocol producer/consumer extraction
Each protocol has a *producer* side and a *consumer* side, extracted with different per-language queries, but both emit a node carrying a **normalized canonical key**. This is the convergent pattern (glia's 13 resolvers, logiclens's contract model): cross-service linking is a deterministic key-join, not fuzzy discovery — so the **normalizers are the oracle**.

| resolver | producer | consumer | key | shape |
|---|---|---|---|---|
| **http** | route def | client call | `METHOD /path/{}` (templated, prefix-stripped) | directional |
| **openapi** | spec endpoint | (pairs with http clients) | same http key | directional |
| **grpc** | `.proto` service.method | generated stub call | `pkg.Service/Method` (pkg-agnostic fallback) | directional |
| **pubsub** | publish (Kafka/NATS/MQTT/AMQP) | subscribe | lowercased topic/subject name | directional |
| **graphql** | resolver | client op | operation name (ci) | directional |
| **shared-schema** | exported type | imported type | type name | symmetric `shares_schema` |
| **shared-db** | table/collection def | other repo's def | `flavor:name` | symmetric `shares_table` |
| **config/env** | env def | `process.env.X` read | var NAME | symmetric `reads_config` |
| **package** | published name | manifest dep | `ecosystem:name` | symmetric `depends_on` |

Implemented first: **http, grpc, pubsub (Kafka + NATS)** (V1). Then the rest, one per protocol-deep corpus fixture.

### 3. Resolve — group by key, pair across repos
`GROUP BY contract_key` over producers + consumers → **cross-edges** (with `confidence`). Symmetric "shares" resolvers emit an edge only when a key bucket spans ≥2 worlds. `/health`, `/ping`, and generic topic names are noise-filtered to avoid N×M false positives.

### 4. Emit — the graph + a gravity report
Output is `graph.Graph` (`worlds / nodes / edges / cross_edges`) as JSON — see `internal/graph`. Plus a basic **gravity / modularity report** (the relatedness metric + the black-hole/modularity score) so the tool is standalone-useful. The rich, persistent layer (storage, hybrid search, the 3D render, navigation at scale) lives in the consumer (below).

## The cosmology (the why)

The output is a map you navigate by force. **Gravity** = the weighted strength of ties between two worlds (a shared DB is heavy, one HTTP call is light); a world's **mass** = its total edge weight. **Modularity** over the gravity graph tells healthy galaxy (clear clusters, sparse links) from **black hole** (everything uniformly bound, no clusters — a distributed monolith you can't pull one service out of). The payoff is **lensing**: from where you stand, pull in only what's gravitationally nearest, up to a token budget, zoomable universe → galaxy → star-system → world → moon — *orbit, don't ingest.* That's how a tool (or an AI) sees a system too big to hold.

## Companion: pg-ai-stewards

lodestar is the *extractor*; the emitted `graph.Graph` JSON imports into [pg-ai-stewards](https://github.com/cpuchip/pg-ai-stewards) (`import_code_graph` + the cross-service resolver), which provides the persistent Postgres world-graph, hybrid (RRF) search, the gravity/black-hole analysis at scale, and the render. The node `kind`s and the `contract_key` normalizers are kept byte-for-byte consistent between the two (e.g. `NormalizeHTTPKey` here ≙ `stewards.normalize_http_key`) so they agree on what "the same endpoint" means. lodestar stands alone; stewards is the home for the data when you want it persistent and queryable.

## Development model

We can't develop on the private target (a ~269-repo distributed monolith), so we develop in the open against a **corpus of public polyglot multi-protocol systems** (see [`corpus.md`](corpus.md)) — and the target's owner refines on the private repos and contributes the generic improvements (a framework's route syntax, a normalization edge case, a black-hole-tuning) back as PRs. The hardest target sharpens the public tool; nothing private leaks.

## Consuming: the pg-ai-stewards import path

The emitted `graph.Graph` JSON imports into pg-ai-stewards via **`stewards.import_lodestar_graph(project, graph_json)`** (extension chain file `83-code-graph.sql`): it imports each world's structure and lands lodestar's already-computed `cross_edges` directly into `cross_world_edges`. lodestar is the single deterministic extraction authority; the substrate stores + serves (persistence, hybrid search, render). Node kinds and the `contract_key` normalizers are kept consistent across the boundary (`NormalizeHTTPKey` ≙ `stewards.normalize_http_key`).

## Phasing

- **V1 — SHIPPED.** Go + TS/JS + Python; **http + grpc + pubsub (Kafka, NATS)**; the gravity/black-hole diagnostic (Louvain modularity, synthetic-graph oracle); every layer oracle-gated. Proven on `open-telemetry/opentelemetry-demo` — real cross-language cross-service edges (Py↔Go, TS↔Go, TS↔Py), zero false positives. Imports into pg-ai-stewards (above).
- **then** — the remaining resolvers (OpenAPI, GraphQL, MQTT/AMQP, shared-schema/DB, config/env, package), one per protocol-deep fixture; the real-repo black-hole proof against `FudanSELab/train-ticket` (needs the Java parser) calibrated by `spring-petclinic-microservices`; more languages (Java, C#, C++); deep cross-file call resolution.
