# lodestar — development corpus

We can't develop against the private target, so we develop against **public, polyglot, multi-protocol systems** and let the private owner refine + PR back. This is the ranked fixture set (verified to exist 2026-06-30). The principle: each fixture earns its place by exercising a specific resolver across a real language boundary — not by being big.

A fixture is only useful if we can **check the answer**. So each one names what the *true* cross-service edges are (from its own docs / architecture), and that expected set is the resolver's oracle.

## Tier A — V1 anchors (build against these first)

| # | repo | why | exercises |
|---|---|---|---|
| **A1** | `open-telemetry/opentelemetry-demo` | **the V1 fixture.** ~12 languages (Go, TS/JS, Python, Java, C#, Rust, …), services talk over **gRPC and HTTP**, with a **Kafka** valuation/accounting path. Three V1 resolvers in one repo, across real language boundaries. Well-documented service graph = a ready-made oracle. | http · grpc · pubsub(kafka) |
| **A2** | `GoogleCloudPlatform/microservices-demo` (Online Boutique) | 11 services, **gRPC-only** mesh, clean and small. The gRPC resolver's calibration fixture — a known, simple service graph to get `pkg.Service/Method` pairing exactly right before the noisy ones. | grpc |

## Tier B — the diagnostic pair (proves the gravity/black-hole report)

| # | repo | role |
|---|---|---|
| **B1** | `FudanSELab/train-ticket` | **the public ball-of-mud.** ~41 microservices, Java/Spring + Python + Node, HTTP REST mesh dense enough to be a research benchmark for microservice coupling. This is the public stand-in for the private distributed monolith — the thing the **black-hole / modularity** diagnostic must light up. |
| **B5** | `spring-petclinic/spring-petclinic-microservices` | **the negative control.** A small, *deliberately clean* Spring Cloud system (clear service boundaries, config server, gateway). Modularity here must score *healthy*. If train-ticket and petclinic don't separate on the diagnostic, the diagnostic is wrong. |

The pair is the inverse hypothesis baked into the corpus: a real black hole and a real healthy galaxy. The metric has to tell them apart.

## Tier C — protocol-deep fixtures (one per resolver, as we add them past V1)

| protocol | fixture | note |
|---|---|---|
| **NATS** | `nats-io/nats.go` examples + a demo app | NATS is thin in the wild — pub/sub subject extraction is straightforward but real multi-service NATS corpora are scarce; flagged as a known gap. |
| **MQTT** | `thingsboard/thingsboard` | MQTT shows up mostly in IoT platforms; ThingsBoard is the one substantial public example. Heavy Java. |
| **GraphQL** | a public Apollo federation demo | federated subgraphs = the cross-service GraphQL case. |
| **OpenAPI** | any A-tier repo's `openapi.yaml` | pairs spec endpoints with discovered http clients — no separate fixture needed. |
| **shared-DB / schema** | train-ticket (shared MySQL) | doubles as a `shares_table` fixture. |

## Honest gaps (named, not hidden)

- **NATS corpora are thin** — the resolver is simple, but well-connected public multi-service NATS systems are rare. V1 ships the resolver; the fixture is weaker than Kafka's.
- **MQTT lives almost only in IoT platforms** (ThingsBoard). Fine for one fixture, not a variety.
- **Swift is absent** from the polyglot microservice corpus — no good public Swift-backend multi-service system surfaced. Swift backends parse fine; we just can't *prove* a cross-service Swift edge on public code yet.
- **Thrift** appears in some systems but is **not** in our V1 resolver list — note it, don't claim it.
- **dapr-style systems** express bindings/pubsub as **YAML component manifests**, not in code — a different extraction surface (config-as-contract) we may want later.
- **Licensing:** prefer Apache-2.0 / MIT fixtures (all Tier A/B are). Some observability stacks (e.g. Sentry) are FSL/BSL — usable as read-only fixtures, but we don't vendor their code.

## How fixtures are used

Cloned under `corpus/` (gitignored — we don't vendor other people's repos). A `corpus.txt` manifest (repo → commit-pin) makes the clone reproducible, so an extraction is rerunnable against the exact same tree (the inverse-hypothesis requirement: re-run after a normalizer change, confirm the expected edge set is unchanged except where intended).
