package parse

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/protobuf"

	"github.com/cpuchip/lodestar/internal/graph"
)

// protoLanguage configures .proto extraction. A .proto that declares a gRPC
// service is recorded as a SCHEMA node (kind=schema) — NOT a producer.
//
// Why not a producer: in the wild the same shared .proto (defining every service)
// is often copied into every service's repo for codegen (e.g. Online Boutique),
// so "this world has a .proto declaring AdService" is NOT evidence this world
// *serves* AdService — it would make every world a false producer of every
// service (an N×M cartesian blowup at resolve time). The authoritative producer
// signal is the server REGISTRATION in hand-written code (Register<Svc>Server /
// add_<Svc>Servicer_to_server), which the per-language gRPC extractors catch.
// The proto still records the contract (service name + methods) for navigation
// and future method-level matching; it just doesn't pair.
//
// Grammar (probed): source_file → package(full_ident) · service(service_name, rpc*)
// · rpc(rpc_name, message_or_enum_type...).
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
		// Schema, not producer: a copied .proto ≠ evidence this world serves it.
		p.addContract(graph.KindSchema, name, meta)
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
