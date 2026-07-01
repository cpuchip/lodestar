package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractJavaGRPC finds gRPC producers and consumers in Java via grpc-java's
// generated base class and stub factories, which are exact and unambiguous:
//
//	class Impl extends AdServiceGrpc.AdServiceImplBase   → producer of AdService
//	AdServiceGrpc.newBlockingStub(channel)               → consumer of AdService
//	AdServiceGrpc.newStub / newFutureStub                → consumer of AdService
//
// The service name is recovered by stripping the generated suffix ("ImplBase" off
// the base class, "Grpc" off the stub-factory owner), matched at the bare
// SERVICE-NAME level so a Java server pairs with a Go/Python/proto peer of the same
// name. This is the signal that lets Online Boutique's Java adservice register as a
// producer of AdService and pair with the Go frontend's NewAdServiceClient.
func extractJavaGRPC(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "class_declaration":
			sc := n.ChildByFieldName("superclass")
			if sc == nil {
				return
			}
			for i := 0; i < int(sc.NamedChildCount()); i++ {
				base := javaTypeName(p, sc.NamedChild(i))
				if base == "" {
					continue
				}
				if svc, ok := javaGRPCServiceFromImplBase(base); ok {
					p.addContract(graph.KindGRPCService, svc, nil)
				}
				break
			}
		case "method_invocation":
			object, name := javaCallTarget(p, n)
			if svc, ok := javaGRPCServiceFromStub(object, name); ok {
				p.addContract(graph.KindGRPCClient, svc, nil)
			}
		}
	})
}

// javaGRPCServiceFromImplBase extracts the service from a <Svc>ImplBase base class.
func javaGRPCServiceFromImplBase(base string) (string, bool) {
	const suf = "ImplBase"
	if strings.HasSuffix(base, suf) && len(base) > len(suf) {
		svc := base[:len(base)-len(suf)]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}

// javaGRPCServiceFromStub extracts the service from a <Svc>Grpc.new*Stub(...) call.
func javaGRPCServiceFromStub(object, name string) (string, bool) {
	switch name {
	case "newBlockingStub", "newStub", "newFutureStub":
	default:
		return "", false
	}
	const suf = "Grpc"
	if strings.HasSuffix(object, suf) && len(object) > len(suf) {
		svc := object[:len(object)-len(suf)]
		if grpcNonServices[svc] {
			return "", false
		}
		return svc, true
	}
	return "", false
}
