// Package resolve is the key-join: it pairs producer nodes in one world with
// consumer nodes in another that carry the same normalized contract key, and
// emits the cross-world edges that make the graph more than a pile of single-repo
// graphs. This is deterministic — a group-by over the keys the contract extractors
// already computed, not fuzzy discovery — which is exactly why the normalizers had
// to be exact.
package resolve

import (
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
}

// Resolve appends cross-world edges to g for every producer/consumer pair that
// shares a contract key across different worlds. Same-world pairs are skipped
// (that's an internal call, not a service boundary); noise keys (health/infra
// endpoints every service has) are skipped to avoid N×M false edges.
func Resolve(g *graph.Graph) {
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
