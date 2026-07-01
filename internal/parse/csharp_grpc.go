package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractCSharpGRPC finds gRPC producers and consumers in C# via Grpc.Tools'
// generated base class and client, keyed at the bare SERVICE-NAME level so a C#
// server pairs with a Go/Python/Java/proto peer of the same name:
//
//	class Impl : Hipstershop.CartService.CartServiceBase   → producer of CartService
//	new Hipstershop.CartService.CartServiceClient(channel) → consumer of CartService
//
// The generated types are nested as <Svc>.<Svc>Base and <Svc>.<Svc>Client, so the
// last two qualified segments must agree after stripping the suffix — this is the
// precision guard that keeps a plain `new HttpClient()` or an ASP.NET base class
// out of the graph. This is the signal that registers Online Boutique's C#
// cartservice as a producer of CartService and pairs it with the Go frontend's
// NewCartServiceClient.
func extractCSharpGRPC(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "class_declaration", "struct_declaration":
			for _, bl := range namedChildrenOfType(n, "base_list") {
				for i := 0; i < int(bl.NamedChildCount()); i++ {
					if svc, ok := csGRPCServiceFromBase(p, bl.NamedChild(i)); ok {
						p.addContract(graph.KindGRPCService, svc, nil)
					}
				}
			}
		case "object_creation_expression":
			if svc, ok := csGRPCServiceFromClientCtor(p, n); ok {
				p.addContract(graph.KindGRPCClient, svc, nil)
			}
		}
	})
}

// csGRPCServiceFromBase extracts the service from a <Svc>.<Svc>Base base type.
func csGRPCServiceFromBase(p *fileCtx, n *sitter.Node) (string, bool) {
	return csGRPCServiceFromSuffixed(p, n, "Base")
}

// csGRPCServiceFromClientCtor extracts the service from a new <Svc>.<Svc>Client(...).
func csGRPCServiceFromClientCtor(p *fileCtx, n *sitter.Node) (string, bool) {
	var typ *sitter.Node
	for i := 0; i < int(n.NamedChildCount()); i++ {
		switch c := n.NamedChild(i); c.Type() {
		case "qualified_name", "identifier", "generic_name":
			typ = c
		}
		if typ != nil {
			break
		}
	}
	if typ == nil {
		return "", false
	}
	return csGRPCServiceFromSuffixed(p, typ, "Client")
}

// csGRPCServiceFromSuffixed pulls <Svc> out of a qualified type whose final
// segment is <Svc><suffix> and whose second-to-last segment is <Svc> — the shape
// grpc codegen emits (Foo.FooBase / Foo.FooClient). Requiring the two segments to
// agree is the precision guard; a bare or mismatched name yields no service.
func csGRPCServiceFromSuffixed(p *fileCtx, n *sitter.Node, suffix string) (string, bool) {
	segs := csQualifiedSegments(p, n)
	if len(segs) < 2 {
		return "", false
	}
	last := segs[len(segs)-1]
	prev := segs[len(segs)-2]
	if !strings.HasSuffix(last, suffix) || len(last) <= len(suffix) {
		return "", false
	}
	svc := last[:len(last)-len(suffix)]
	if svc != prev {
		return "", false
	}
	if grpcNonServices[svc] {
		return "", false
	}
	return svc, true
}
