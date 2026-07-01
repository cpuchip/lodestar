package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractTSGRPC finds gRPC consumers in TS/JS via the grpc-js client constructor:
//
//	new ShippingServiceClient(addr, creds)          → consumer of ShippingService
//	new proto.hipstershop.ShippingServiceClient(..) → consumer of ShippingService
//
// The constructor name (bare identifier or the final property of a member access)
// ending in "Client" yields the bare service name, package-agnostic, so it pairs
// with a Go/proto/Python service of the same name. The grpcNonServices denylist
// (shared with the Go extractor) keeps generic *Client ctors out.
//
// DEFERRED: the producer side (server.addService(XService, impl)) — grpc-js passes
// a service *descriptor object*, not a clean service name, so there is no reliable
// static token to key on. Covering it would need per-framework descriptor tracing;
// the .proto extractor already supplies the producer key for a TS server that ships
// its own .proto, which is the common otel-demo shape.
func extractTSGRPC(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "new_expression" {
			return
		}
		name := tsNewName(p, n)
		if svc, ok := tsGRPCServiceFromClient(name); ok {
			p.addContract(graph.KindGRPCClient, svc, nil)
		}
	})
}

// tsNewName returns the constructor name of a new_expression: the identifier, or
// the final property of a member expression (proto.pkg.XClient → "XClient").
func tsNewName(p *fileCtx, ne *sitter.Node) string {
	c := ne.ChildByFieldName("constructor")
	if c == nil {
		return ""
	}
	switch c.Type() {
	case "identifier":
		return c.Content(p.src)
	case "member_expression":
		if pr := c.ChildByFieldName("property"); pr != nil {
			return pr.Content(p.src)
		}
	}
	return ""
}

// tsGRPCServiceFromClient extracts the service name from new XClient.
func tsGRPCServiceFromClient(name string) (string, bool) {
	const suf = "Client"
	if strings.HasSuffix(name, suf) && len(name) > len(suf) {
		svc := name[:len(name)-len(suf)]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}
