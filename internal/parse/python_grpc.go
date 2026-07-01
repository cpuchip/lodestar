package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractPythonGRPC finds gRPC producers and consumers in Python via the generated
// registrar/stub names, which are exact and unambiguous:
//
//	add_ProductCatalogServiceServicer_to_server(impl, server) → producer of ProductCatalogService
//	ShippingServiceStub(channel)                              → consumer of ShippingService
//
// Matched at the SERVICE-NAME level (package-agnostic), so a Python stub pairs with
// a Go/proto service of the same name. The name may be called bare or off a
// *_pb2_grpc module (nc.<name>) — pyCallTarget yields the final segment either way.
func extractPythonGRPC(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		_, name := pyCallTarget(p, n)
		if name == "" {
			return
		}
		if svc, ok := pyGRPCServiceFromStub(name); ok {
			p.addContract(graph.KindGRPCClient, svc, nil)
		} else if svc, ok := pyGRPCServiceFromRegistrar(name); ok {
			p.addContract(graph.KindGRPCService, svc, nil)
		}
	})
}

// pyGRPCServiceFromRegistrar extracts the service from add_XServicer_to_server.
func pyGRPCServiceFromRegistrar(name string) (string, bool) {
	const pre, suf = "add_", "Servicer_to_server"
	if strings.HasPrefix(name, pre) && strings.HasSuffix(name, suf) && len(name) > len(pre)+len(suf) {
		svc := name[len(pre) : len(name)-len(suf)]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}

// pyGRPCServiceFromStub extracts the service from XStub. The grpcNonServices
// denylist (shared with the Go extractor) keeps generic *Stub names out.
func pyGRPCServiceFromStub(name string) (string, bool) {
	const suf = "Stub"
	if strings.HasSuffix(name, suf) && len(name) > len(suf) {
		svc := name[:len(name)-len(suf)]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}
