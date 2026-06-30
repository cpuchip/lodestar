# lodestar

**Navigate any codebase by its gravity.**

`lodestar` is a native, multi-language, multi-protocol **cross-service code-graph extractor**. Point it at one repo or a hundred, and it maps every service and the lines of force between them — HTTP, gRPC, Kafka, NATS, MQTT, GraphQL, shared schemas — into one graph you can actually navigate.

## The problem

A system grows past the point any one person — or any one AI context window — can hold. A few dozen repos become a few hundred; the call graph crosses every boundary; "what calls this?" takes an afternoon of grep. A distributed system with no visible clusters and uniform coupling everywhere stops being a galaxy and becomes **a black hole** — you can't pull one service out without the whole thing collapsing inward, and you can't see in.

`lodestar` puts a number and a map on that. It finds the **gravity** between services (the weighted edges of real cross-service calls), flags the **black holes** (the modularity score, the singularity services everything orbits), and lets a tool — human or AI — **orbit instead of ingest**: from where you stand, pull in only what's gravitationally nearest, up to a budget, zoomable from the whole universe down to a single function.

## The cosmology

| term | what it is |
|---|---|
| **universe** | an org / a coherent body of work |
| **galaxy** | a platform |
| **star system** | a sub-system — a cluster of related services |
| **world** | a service / repo / bounded context |
| **moon** | a module / file / function inside the service |
| **multiverse** | disconnected components — universes with no edges between them (yet) |
| **gravity** | the weighted strength of the ties between two worlds |
| **black hole** | a ball-of-mud: everything uniformly bound, no clusters, no escape |
| **lensing** | bending light around the mass to see what it hides — the navigation |

## How it works

1. **Parse** — `go-tree-sitter`, config-driven per language → structural entities + edges (file / class / function; contains / imports / calls / inherits). Deterministic; no LLM, so it can neither fabricate nor loop.
2. **Contracts** — per-protocol extractors emit a *producer* side and a *consumer* side, each carrying a **normalized canonical key** (`GET /users/{}`, `pkg.Service/Method`, a topic name): HTTP, OpenAPI, gRPC, Kafka/NATS/MQTT, GraphQL, shared-schema, shared-DB, config/env, package.
3. **Resolve** — group producers + consumers by key and pair them across repos → **cross-service edges**, confidence-graded. Cross-service linking is a deterministic key-join, not fuzzy discovery — so the normalizer *is* the oracle.
4. **Emit** — a clean graph (`worlds / nodes / edges / cross_edges`) + a basic gravity/modularity report. Standalone-useful; also the import format for richer stores (see below).

## Status

Early, and built in the open. The deterministic key-normalizers come first (the floor everything stands on), then the parsers and contract extractors, language by language, protocol by protocol — developed against a corpus of public polyglot microservices systems. See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Companion

`lodestar` is the *extractor*. Its emitted graph imports cleanly into [pg-ai-stewards](https://github.com/cpuchip/pg-ai-stewards), which provides the persistent world-graph store, hybrid search, the gravity/black-hole analysis at scale, and the 3D render — but `lodestar` stands alone: point it at your repos and read your own sky.

## License

MIT — see [LICENSE](LICENSE).
