// Package graph is the lodestar output model: the worlds, their entities and
// intra-world edges, and the cross-service ties between them. It is the contract
// between the extractor and any consumer (a JSON store, pg-ai-stewards'
// import_code_graph, a renderer). Keep it small and stable.
package graph

// Node is a code entity — a "moon" in the cosmology: a file, class, function,
// endpoint, client, topic producer/consumer, schema, etc. ID is stable within a
// single extraction (e.g. "repo::path::symbol") and is what edges reference.
// Name is the qualified, human-facing identity, unique within (World, Kind) —
// it is also the dedup key on the consuming side.
type Node struct {
	ID       string            `json:"id"`
	World    string            `json:"world"` // the service / repo this lives in
	Kind     string            `json:"kind"`  // see Kind* constants
	Name     string            `json:"name"`
	Summary  string            `json:"summary,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"` // e.g. method/path for http; topic for pubsub
}

// Edge is an intra-world relationship (within one service).
type Edge struct {
	Src string `json:"src"` // Node.ID
	Dst string `json:"dst"` // Node.ID
	Rel string `json:"rel"` // see Rel* constants
}

// CrossEdge links two entities across worlds — the cross-service tie that makes
// the graph more than a pile of single-repo graphs. ContractKey is the normalized
// key that paired the producer and consumer.
type CrossEdge struct {
	Src         string  `json:"src"`          // producer Node.ID
	Dst         string  `json:"dst"`          // consumer Node.ID
	Rel         string  `json:"rel"`          // http_calls | grpc_calls | publishes_to | consumes_from | shares_schema ...
	Protocol    string  `json:"protocol"`     // http | grpc | pubsub | graphql | schema | db | config | package
	ContractKey string  `json:"contract_key"` // the normalized key that matched them
	Confidence  float64 `json:"confidence"`   // 1.0 extracted / 0.55–0.95 inferred
}

// WorldMeta is per-world git provenance: where the repo came from and what ref
// was extracted. Both fields are best-effort — a non-git directory (or a repo
// without an origin remote) yields empty strings, and the caller omits the world
// from world_meta entirely. Feeds pg-ai-stewards' import_lodestar_graph
// (p_repo_origins[w] = RepoOrigin, p_ref = a chosen Ref) so the substrate can
// stamp origin+ref onto the world/entities (branch-aware world-graph, #298).
type WorldMeta struct {
	RepoOrigin string `json:"repo_origin,omitempty"` // git remote origin URL
	Ref        string `json:"ref,omitempty"`         // branch name, or short SHA if detached
}

// Graph is the whole emitted artifact. WorldMeta is additive (an unknown top-level
// key existing consumers ignore); it is omitted entirely when no world had git
// provenance to capture.
type Graph struct {
	Worlds     []string             `json:"worlds"`
	Nodes      []Node               `json:"nodes"`
	Edges      []Edge               `json:"edges"`
	CrossEdges []CrossEdge          `json:"cross_edges"`
	WorldMeta  map[string]WorldMeta `json:"world_meta,omitempty"`
}

// Entity kinds.
const (
	KindFile          = "file"
	KindModule        = "module"
	KindClass         = "class"
	KindFunction      = "function"
	KindMethod        = "method"
	KindInterface     = "interface"
	KindHTTPEndpoint  = "http_endpoint"  // producer (a route)
	KindHTTPClient    = "http_client"    // consumer (a call)
	KindGRPCService   = "grpc_service" // producer (a .proto service / RegisterXServer)
	KindGRPCClient    = "grpc_client"  // consumer (NewXClient)
	KindGRPCMethod    = "grpc_method"
	KindTopicProducer = "topic_producer" // pub
	KindTopicConsumer = "topic_consumer" // sub
	KindSchema        = "schema"
	KindConfigKey     = "config_key"  // an env var a service reads (symmetric coupling)
	KindDataEntity    = "data_entity" // a DB table a service touches (symmetric coupling)
	KindPackage       = "package"
)

// Intra-world edge relations.
const (
	RelContains   = "contains"
	RelImports    = "imports"
	RelCalls      = "calls"
	RelInherits   = "inherits"
	RelImplements = "implements"
)
