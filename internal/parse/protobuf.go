package parse

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/protobuf"

	"github.com/cpuchip/lodestar/internal/graph"
)

// protoLanguage configures .proto extraction. A .proto that declares a gRPC
// service is the contract's authority — the service that ships it is the producer.
// Grammar (probed): source_file → package(full_ident) · service(service_name, rpc*)
// · rpc(rpc_name, message_or_enum_type...).
//
// The producer key is the SERVICE NAME alone (package-agnostic): a Go consumer
// constructs its client with NewProductCatalogServiceClient(...), which carries
// the service name but NOT the proto package, so the service name is the only
// token both sides share. Methods + package are kept as metadata for later
// method-level refinement.
func protoLanguage() Language {
	return Language{
		Name:    "protobuf",
		Exts:    []string{".proto"},
		grammar: protobuf.GetLanguage(),
		extract: extractProto,
	}
}

func extractProto(p *fileCtx, root *sitter.Node) {
	pkg := ""
	for i := 0; i < int(root.NamedChildCount()); i++ {
		if root.NamedChild(i).Type() == "package" {
			pkg = firstChildText(p, root.NamedChild(i), "full_ident")
			break
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		svc := root.NamedChild(i)
		if svc.Type() != "service" {
			continue
		}
		name := firstChildText(p, svc, "service_name")
		if name == "" {
			continue
		}
		var methods []string
		for _, rpc := range namedChildrenOfType(svc, "rpc") {
			if m := firstChildText(p, rpc, "rpc_name"); m != "" {
				methods = append(methods, m)
			}
		}
		meta := map[string]string{}
		if pkg != "" {
			meta["package"] = pkg
		}
		if len(methods) > 0 {
			meta["methods"] = joinSpace(methods)
		}
		p.addContract(graph.KindGRPCService, name, meta)
	}
}

// firstChildText returns the text of n's first named child of the given type.
func firstChildText(p *fileCtx, n *sitter.Node, typ string) string {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == typ {
			return c.Content(p.src)
		}
	}
	return ""
}

func joinSpace(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
