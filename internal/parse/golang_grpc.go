package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractGoGRPC finds gRPC producers and consumers in Go via the generated
// constructor/registrar names, which are exact and unambiguous:
//
//	RegisterProductCatalogServiceServer(s, impl)  → producer of ProductCatalogService
//	NewProductCatalogServiceClient(conn)          → consumer of ProductCatalogService
//
// Matched at the SERVICE-NAME level (package-agnostic), because the Go client
// constructor carries the service name but not the proto package. Precision comes
// from the resolve-time key-join: a stray NewRedisClient only produces an edge if
// some world actually declares a "Redis" gRPC service, which it won't.
func extractGoGRPC(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		_, verb := goCallTarget(p, n)
		if verb == "" {
			return
		}
		if svc, ok := grpcServiceFromClientCtor(verb); ok {
			p.addContract(graph.KindGRPCClient, svc, nil)
		} else if svc, ok := grpcServiceFromServerReg(verb); ok {
			p.addContract(graph.KindGRPCService, svc, nil)
		}
	})
}

// grpcNonServices are common New*Client / Register*Server names that are NOT gRPC
// services, kept out of the graph even though the join would already drop them.
var grpcNonServices = map[string]bool{
	"": true, "HTTP": true, "Http": true, "Redis": true, "S3": true, "SQL": true,
	"DB": true, "Grpc": true, "GRPC": true, "Mongo": true, "Kafka": true, "AWS": true,
	"GCP": true, "OAuth2": true,
}

// grpcServiceFromClientCtor extracts the service name from NewXxxClient.
func grpcServiceFromClientCtor(name string) (string, bool) {
	if strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Client") && len(name) > len("NewClient") {
		svc := name[len("New") : len(name)-len("Client")]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}

// grpcServiceFromServerReg extracts the service name from RegisterXxxServer.
func grpcServiceFromServerReg(name string) (string, bool) {
	if strings.HasPrefix(name, "Register") && strings.HasSuffix(name, "Server") && len(name) > len("RegisterServer") {
		svc := name[len("Register") : len(name)-len("Server")]
		if grpcNonServices[svc] || svc == "Health" || svc == "ServerReflection" {
			return "", false
		}
		return svc, true
	}
	return "", false
}
