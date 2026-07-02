// Package resolve is the key-join: it pairs producer nodes in one world with
// consumer nodes in another that carry the same normalized contract key, and
// emits the cross-world edges that make the graph more than a pile of single-repo
// graphs. This is deterministic — a group-by over the keys the contract extractors
// already computed, not fuzzy discovery — which is exactly why the normalizers had
// to be exact.
package resolve

import (
	"sort"
	"strings"

	"github.com/cpuchip/lodestar/internal/graph"
)

// pairing describes how one protocol's producer and consumer node kinds link.
type pairing struct {
	producerKind string
	consumerKind string
	rel          string
	protocol     string
	// srcIsProducer sets edge direction. HTTP/gRPC: the consumer (caller) initiates,
	// so src=consumer, dst=producer (the dependency points caller→callee). Pub-sub:
	// data flows publisher→subscriber, so src=producer.
	srcIsProducer bool
	confidence    float64
	isNoise       func(key string) bool
}

// pairings is the registry. gRPC and pub-sub append here as their extractors land.
var pairings = []pairing{
	{
		producerKind:  graph.KindHTTPEndpoint,
		consumerKind:  graph.KindHTTPClient,
		rel:           "http_call",
		protocol:      "http",
		srcIsProducer: false,
		confidence:    0.85,
		isNoise:       isHTTPNoise,
	},
	{
		producerKind:  graph.KindGRPCService,
		consumerKind:  graph.KindGRPCClient,
		rel:           "grpc_call",
		protocol:      "grpc",
		srcIsProducer: false,
		confidence:    0.9, // service names are specific → a match is strong
		isNoise:       nil,
	},
	{
		producerKind:  graph.KindTopicProducer,
		consumerKind:  graph.KindTopicConsumer,
		rel:           "publishes_to",
		protocol:      "pubsub",
		srcIsProducer: true, // data flows publisher → subscriber
		confidence:    0.8,  // topic names can be generic → slightly lower
		isNoise:       nil,
	},
	{
		// Deployment topology from Helm: a chart defines a service (producer); a
		// values/env service reference names an upstream (consumer). The referencing
		// service depends on the target, so src=consumer, dst=producer.
		producerKind:  graph.KindService,
		consumerKind:  graph.KindServiceRef,
		rel:           "connects_to",
		protocol:      "k8s",
		srcIsProducer: false,
		confidence:    0.8,
		isNoise:       isServiceRefNoise,
	},
	{
		// Library backbone: a repo's go.mod require ↔ the repo that publishes that
		// module. The requiring repo depends_on the published module. A shared lib
		// pulled into many repos becomes a heavy gravity center (protocol `package`,
		// weight 1.5) — the compile-time coupling the runtime resolvers miss.
		producerKind:  graph.KindPackagePublish,
		consumerKind:  graph.KindPackageDep,
		rel:           "depends_on",
		protocol:      "package",
		srcIsProducer: false, // consumer (requiring repo) → producer (published module)
		confidence:    0.95,  // a go.mod require is an exact, certain dependency
		isNoise:       nil,
	},
}

// infraServices are shared infrastructure a values ref may name (a DB, broker,
// cache, telemetry sink) that is NOT an application service in the graph. The
// key-join already drops any target no chart PRODUCES, so this is belt-and-braces
// against a repo that happens to vendor an infra chart by a generic name.
var infraServices = map[string]bool{
	"redis": true, "postgres": true, "postgresql": true, "mysql": true, "mariadb": true,
	"mongo": true, "mongodb": true, "cassandra": true, "kafka": true, "nats": true,
	"rabbitmq": true, "memcached": true, "etcd": true, "vault": true, "consul": true,
	"zookeeper": true, "elasticsearch": true, "opensearch": true, "prometheus": true,
	"grafana": true, "jaeger": true, "localhost": true, "db": true, "cache": true,
	"database": true, "queue": true,
}

// isServiceRefNoise drops references to shared infrastructure (not app services).
func isServiceRefNoise(key string) bool { return infraServices[key] }

// symmetricPairing describes a coupling with no producer/consumer direction: a key
// (an env var, a table name) that appears in ≥2 worlds means those worlds are bound.
// Unlike a directional pairing there is one kind, and the edge is undirected.
type symmetricPairing struct {
	kind       string
	rel        string
	protocol   string
	confidence float64
	isNoise    func(key string) bool
}

// maxSymmetricWorlds caps how many worlds a symmetric key may span before it is
// treated as generic infrastructure rather than a meaningful coupling. A var/table
// in 7+ services (PORT, LOG_LEVEL, a shared "users" table in every app) is shared
// plumbing, not a distributed-monolith seam — and pairing it across every world is
// the #1 source of edge explosion. This cap is mandatory, not tunable-to-taste.
const maxSymmetricWorlds = 6

// symmetricPairings is the registry for the heavy-coupling resolvers. Weights live
// in the gravity package (config 1.5, db 3.0) keyed by Protocol.
var symmetricPairings = []symmetricPairing{
	{kind: graph.KindConfigKey, rel: "reads_config", protocol: "config", confidence: 0.7, isNoise: isConfigNoise},
	{kind: graph.KindDataEntity, rel: "shares_table", protocol: "db", confidence: 0.75, isNoise: isTableNoise},
	// shared-schema (type-name collisions: User, Config, Options) is DEFERRED — too
	// noisy across services to resolve precisely without a real type identity. A
	// directional protocol (schema, weight 2.0) is reserved for it in gravity.
	// TODO(schema): add only with a precise key (package-qualified type), never bare names.
}

// Resolve appends cross-world edges to g for every producer/consumer pair that
// shares a contract key across different worlds, then the symmetric heavy couplings
// (shared config/env, shared DB tables). Same-world pairs are skipped (that's an
// internal call, not a service boundary); noise keys (health/infra endpoints,
// ubiquitous env vars, catalog tables) are skipped to avoid N×M false edges.
func Resolve(g *graph.Graph) {
	resolveDirectional(g)
	resolveSymmetric(g)
}

// resolveDirectional pairs producer↔consumer kinds (HTTP/gRPC/pub-sub).
func resolveDirectional(g *graph.Graph) {
	for _, pr := range pairings {
		producers := map[string][]graph.Node{}
		consumers := map[string][]graph.Node{}
		for _, n := range g.Nodes {
			switch n.Kind {
			case pr.producerKind:
				producers[n.Name] = append(producers[n.Name], n)
			case pr.consumerKind:
				consumers[n.Name] = append(consumers[n.Name], n)
			}
		}
		for key, prods := range producers {
			cons := consumers[key]
			if len(cons) == 0 {
				continue
			}
			if pr.isNoise != nil && pr.isNoise(key) {
				continue
			}
			for _, prod := range prods {
				for _, con := range cons {
					if prod.World == con.World {
						continue // internal call, not a cross-service boundary
					}
					src, dst := con.ID, prod.ID
					if pr.srcIsProducer {
						src, dst = prod.ID, con.ID
					}
					g.CrossEdges = append(g.CrossEdges, graph.CrossEdge{
						Src:         src,
						Dst:         dst,
						Rel:         pr.rel,
						Protocol:    pr.protocol,
						ContractKey: key,
						Confidence:  pr.confidence,
					})
				}
			}
		}
	}
}

// resolveSymmetric emits undirected couplings: for each symmetric pairing it groups
// nodes of the kind by their key (env var / table name), and for a key that appears
// in ≥2 (and ≤maxSymmetricWorlds) distinct worlds it links each unordered pair of
// worlds ONCE. Explosion is bounded three ways: (1) one representative node per
// world collapses many files reading one var to a single endpoint, so a key over W
// worlds yields exactly C(W,2) edges — at the W≤6 cap, ≤15; (2) a key in one world,
// or over the cap, is skipped; (3) the isNoise denylist drops ubiquitous keys.
// Deterministic: worlds and keys are traversed in sorted order, and the src<dst
// world ordering means each pair is emitted exactly once (never both a→b and b→a).
func resolveSymmetric(g *graph.Graph) {
	for _, sp := range symmetricPairings {
		// key -> world -> representative node ID (lexicographically smallest, so many
		// files in one world collapse to a single deterministic endpoint per world).
		byKey := map[string]map[string]string{}
		for _, n := range g.Nodes {
			if n.Kind != sp.kind {
				continue
			}
			worlds := byKey[n.Name]
			if worlds == nil {
				worlds = map[string]string{}
				byKey[n.Name] = worlds
			}
			if cur, ok := worlds[n.World]; !ok || n.ID < cur {
				worlds[n.World] = n.ID
			}
		}

		keys := make([]string, 0, len(byKey))
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, key := range keys {
			if sp.isNoise != nil && sp.isNoise(key) {
				continue // generic name (PORT, migrations) — not a coupling
			}
			worldReps := byKey[key]
			if len(worldReps) < 2 {
				continue // a key in a single world is not a cross-service coupling
			}
			if len(worldReps) > maxSymmetricWorlds {
				continue // a key in 7+ worlds is generic infrastructure, not a seam
			}
			worlds := make([]string, 0, len(worldReps))
			for w := range worldReps {
				worlds = append(worlds, w)
			}
			sort.Strings(worlds)
			// Pairwise over worlds, each unordered pair once (i<j → src world < dst world).
			for i := 0; i < len(worlds); i++ {
				for j := i + 1; j < len(worlds); j++ {
					g.CrossEdges = append(g.CrossEdges, graph.CrossEdge{
						Src:         worldReps[worlds[i]],
						Dst:         worldReps[worlds[j]],
						Rel:         sp.rel,
						Protocol:    sp.protocol,
						ContractKey: key,
						Confidence:  sp.confidence,
					})
				}
			}
		}
	}
}

// configNoise is the denylist of ubiquitous env vars that are NOT couplings — every
// service reads them, so pairing on them would bind the whole system. Matched
// case-insensitively; the maxSymmetricWorlds cap catches the long tail this misses.
var configNoise = map[string]bool{
	// system / shell / runtime
	"PORT": true, "HOST": true, "HOSTNAME": true, "ADDR": true, "ADDRESS": true,
	"PATH": true, "HOME": true, "PWD": true, "USER": true, "LANG": true, "TERM": true,
	"TZ": true, "SHELL": true, "DEBUG": true, "VERBOSE": true, "ENV": true,
	"ENVIRONMENT": true, "NODE_ENV": true, "LOG_LEVEL": true, "LOGLEVEL": true,
	"GOPATH": true, "GOROOT": true,
	// platform / observability infra — same class as OTEL_* (telemetry) and
	// DEBUG/VERBOSE (ops toggles): a shared collector endpoint or a profiler on/off
	// flag means "both run on the same platform," not that the services are coupled.
	// (Found leaking into online-boutique's config edges on the real-repo run.)
	"COLLECTOR_SERVICE_ADDR": true, "ENABLE_PROFILER": true, "DISABLE_PROFILER": true,
}

// isConfigNoise reports whether an env var name is ubiquitous infra (not a coupling).
// OTEL_* telemetry vars are excluded wholesale — every otel-demo service sets them.
func isConfigNoise(key string) bool {
	up := strings.ToUpper(key)
	if strings.HasPrefix(up, "OTEL_") {
		return true
	}
	return configNoise[up]
}

// tableNoise is the denylist of DB tables that are not couplings: catalog/metadata
// tables and migration bookkeeping every schema-managed service touches. Names
// arrive lowercased from the extractor.
var tableNoise = map[string]bool{
	"information_schema": true, "pg_catalog": true, "sqlite_master": true,
	"migrations": true, "schema_migrations": true,
}

// isTableNoise reports whether a (lowercased) table name is generic infra. Checks
// the leading schema segment too (information_schema.tables → information_schema)
// and drops 1-2 char names (aliases/fragments that slipped the SQL gate) and the
// pg_/sqlite_ internal-catalog prefixes.
func isTableNoise(key string) bool {
	if len(key) <= 2 {
		return true
	}
	if strings.HasPrefix(key, "pg_") || strings.HasPrefix(key, "sqlite_") {
		return true
	}
	seg := key
	if i := strings.IndexByte(key, '.'); i >= 0 {
		seg = key[:i]
	}
	return tableNoise[key] || tableNoise[seg]
}

// httpNoisePaths are health/infra endpoints nearly every service exposes; matching
// them across services produces meaningless N×M edges, so they're excluded.
var httpNoisePaths = map[string]bool{
	"/": true, "/health": true, "/healthz": true, "/ping": true, "/ready": true,
	"/readyz": true, "/live": true, "/livez": true, "/metrics": true, "/status": true,
	"/version": true, "/favicon.ico": true,
}

// isHTTPNoise reports whether an "METHOD /path" key targets a generic infra path.
func isHTTPNoise(key string) bool {
	path := key
	if i := strings.IndexByte(key, ' '); i >= 0 {
		path = key[i+1:]
	}
	return httpNoisePaths[path]
}
