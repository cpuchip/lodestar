package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"github.com/cpuchip/lodestar/internal/graph"
)

// goLanguage configures Go extraction. Grammar node types referenced:
//
//	function_declaration  name:(identifier)
//	method_declaration    name:(field_identifier) receiver:(parameter_list)
//	type_declaration      → type_spec  name:(type_identifier) type:(struct_type|interface_type|...)
//	import_declaration     → import_spec  path:(interpreted_string_literal)
//
// Only top-level declarations are walked — nested funcs/types are implementation
// detail, not part of the service's public skeleton.
func goLanguage() Language {
	return Language{
		Name:      "go",
		Exts:      []string{".go"},
		grammar:   golang.GetLanguage(),
		extract:   extractGo,
		contracts: []func(*fileCtx, *sitter.Node){extractGoHTTP, extractGoGRPC, extractGoPubSub},
	}
}

func extractGo(p *fileCtx, root *sitter.Node) {
	var imports []string
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "function_declaration":
			if name := p.fieldText(n, "name"); name != "" {
				id := p.addDecl(graph.KindFunction, name, nil)
				p.recordGoCalls(id, n)
			}
		case "method_declaration":
			name := p.fieldText(n, "name")
			if name == "" {
				continue
			}
			recv := goReceiverType(p, n)
			full := name
			meta := map[string]string{}
			if recv != "" {
				full = recv + "." + name
				meta["receiver"] = recv
			}
			id := p.addDecl(graph.KindMethod, full, meta)
			p.recordGoCalls(id, n)
		case "type_declaration":
			for _, spec := range namedChildrenOfType(n, "type_spec") {
				name := p.fieldTextOf(spec, "name")
				if name == "" {
					continue
				}
				kind := graph.KindClass
				if t := spec.ChildByFieldName("type"); t != nil && t.Type() == "interface_type" {
					kind = graph.KindInterface
				}
				p.addDecl(kind, name, nil)
			}
		case "import_declaration":
			imports = append(imports, goImports(p, n)...)
		}
	}
	if len(imports) > 0 {
		// Record imports as file metadata rather than fake nodes: an import target
		// is another world's package, not a node this world owns. The contract
		// layer is what links across worlds.
		for i := range p.g.Nodes {
			if p.g.Nodes[i].ID == p.fileID {
				if p.g.Nodes[i].Metadata == nil {
					p.g.Nodes[i].Metadata = map[string]string{}
				}
				p.g.Nodes[i].Metadata["imports"] = strings.Join(imports, " ")
				break
			}
		}
	}
}

// recordGoCalls records a pending calls ref for every function call in a Go
// function/method body. The callee is the bare identifier (`foo()`) or the final
// selector segment (`s.setup()` -> "setup", `fmt.Println()` -> "Println"); the
// receiver/package operand is intentionally dropped — V1 resolves on the bare name
// and emits only when it is unique in the world, so a stdlib callee like Println
// (no world symbol) yields no edge.
//
// Go inheritance is deliberately NOT modeled: Go has no class inheritance, and
// struct/interface embedding is a materially different relation than
// inherits/implements — better omitted than mislabeled.
func (p *fileCtx) recordGoCalls(declID string, decl *sitter.Node) {
	p.recordCalls(declID, decl.ChildByFieldName("body"), "call_expression", func(n *sitter.Node) string {
		_, verb := goCallTarget(p, n)
		return verb
	})
}

// goReceiverType pulls the receiver type name off a method_declaration, stripping
// a leading pointer star: `func (s *Server) F()` → "Server".
func goReceiverType(p *fileCtx, method *sitter.Node) string {
	recv := method.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	for _, pd := range namedChildrenOfType(recv, "parameter_declaration") {
		t := pd.ChildByFieldName("type")
		if t == nil {
			continue
		}
		txt := t.Content(p.src)
		return strings.TrimPrefix(txt, "*")
	}
	return ""
}

// goImports returns the quoted-stripped import paths in an import_declaration,
// handling both the single `import "x"` and the grouped `import ( ... )` forms.
func goImports(p *fileCtx, decl *sitter.Node) []string {
	var out []string
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "import_spec" {
				if path := c.ChildByFieldName("path"); path != nil {
					out = append(out, strings.Trim(path.Content(p.src), "\"`"))
				}
				continue
			}
			visit(c)
		}
	}
	visit(decl)
	return out
}

// fieldTextOf is fieldText for an arbitrary node (fieldText is file-root relative
// only by convention; both just read a named field's text).
func (p *fileCtx) fieldTextOf(n *sitter.Node, field string) string {
	return p.fieldText(n, field)
}
