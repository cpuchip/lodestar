package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/csharp"

	"github.com/cpuchip/lodestar/internal/graph"
)

// csharpLanguage configures C# extraction. Grammar node types referenced (probed
// against tree-sitter-csharp):
//
//	class_declaration      name:(identifier)  (base_list)?  body:(declaration_list)
//	interface_declaration  name:(identifier)  body:(declaration_list)
//	struct_declaration      name:(identifier)  body:(declaration_list)
//	record_declaration      name:(identifier)  body:(declaration_list)
//	method_declaration      name:(identifier)  body:(block)  (attribute_list)*
//	invocation_expression   function:(member_access_expression|identifier)  arguments:(argument_list)
//	member_access_expression expression:(...)  name:(identifier)
//	element_access_expression expression:(...)  subscript:(bracketed_argument_list)
//	object_creation_expression (type)  arguments:(argument_list)
//	using_directive         → (qualified_name|identifier)
//	namespace_declaration   → (qualified_name) (declaration_list)
//	string_literal          → (string_literal_content)*
//
// Types nest inside namespaces and inside other types, so the pass descends
// through namespace and type bodies. Only type declarations and their direct
// methods become nodes.
func csharpLanguage() Language {
	return Language{
		Name:      "csharp",
		Exts:      []string{".cs"},
		grammar:   csharp.GetLanguage(),
		extract:   extractCSharp,
		contracts: []func(*fileCtx, *sitter.Node){extractCSharpHTTP, extractCSharpGRPC, extractCSharpPubSub, extractCSharpConfig, extractSQLTables},
	}
}

// isCSharpTypeDecl reports whether a node type is a C# type declaration.
func isCSharpTypeDecl(t string) bool {
	switch t {
	case "class_declaration", "interface_declaration", "struct_declaration",
		"record_declaration", "record_struct_declaration", "enum_declaration":
		return true
	}
	return false
}

func extractCSharp(p *fileCtx, root *sitter.Node) {
	var imports []string
	var handleType func(n *sitter.Node)
	handleType = func(n *sitter.Node) {
		name := p.fieldText(n, "name")
		if name == "" {
			return
		}
		kind := graph.KindClass
		if n.Type() == "interface_declaration" {
			kind = graph.KindInterface
		}
		classID := p.addDecl(kind, name, nil)
		p.csHeritage(classID, n)
		body := n.ChildByFieldName("body")
		if body == nil {
			return
		}
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch {
			case m.Type() == "method_declaration":
				if mn := p.fieldText(m, "name"); mn != "" {
					id := p.addDecl(graph.KindMethod, name+"."+mn, map[string]string{"receiver": name})
					p.recordCSharpCalls(id, m)
				}
			case isCSharpTypeDecl(m.Type()):
				handleType(m) // nested type
			}
		}
	}
	// descend walks namespaces / declaration lists down to type declarations.
	var descend func(n *sitter.Node)
	descend = func(n *sitter.Node) {
		switch {
		case isCSharpTypeDecl(n.Type()):
			handleType(n)
		case n.Type() == "using_directive":
			if s := csUsingName(p, n); s != "" {
				imports = append(imports, s)
			}
		case n.Type() == "namespace_declaration" || n.Type() == "file_scoped_namespace_declaration" || n.Type() == "declaration_list":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				descend(n.NamedChild(i))
			}
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		descend(root.NamedChild(i))
	}
	if len(imports) > 0 {
		p.recordImports(imports)
	}
}

// csHeritage records inherits/implements refs from a class/struct base_list. C#
// syntax does not distinguish a base class from an implemented interface, so the
// long-standing C# naming convention is used: an IPascalCase name (I followed by
// an uppercase letter) is an interface → implements; anything else → inherits.
// relAcceptsKind is the safety net: a mislabeled ref that resolves to the wrong
// kind simply yields no edge.
func (p *fileCtx) csHeritage(classID string, n *sitter.Node) {
	for _, bl := range namedChildrenOfType(n, "base_list") {
		for i := 0; i < int(bl.NamedChildCount()); i++ {
			base := csTypeName(p, bl.NamedChild(i))
			if base == "" {
				continue
			}
			if isCSharpInterfaceName(base) {
				p.addRef(classID, base, graph.RelImplements)
			} else {
				p.addRef(classID, base, graph.RelInherits)
			}
		}
	}
}

// isCSharpInterfaceName applies the C# I-prefix interface convention.
func isCSharpInterfaceName(name string) bool {
	return len(name) >= 2 && name[0] == 'I' && name[1] >= 'A' && name[1] <= 'Z'
}

// recordCSharpCalls records a pending calls ref for every invocation in a method
// body: the bare method name (`this.Setup()` -> "Setup", `Foo.Bar()` -> "Bar").
func (p *fileCtx) recordCSharpCalls(declID string, decl *sitter.Node) {
	p.recordCalls(declID, decl.ChildByFieldName("body"), "invocation_expression", func(n *sitter.Node) string {
		_, name := csCallTarget(p, n)
		return name
	})
}

// --- shared C# tree-sitter helpers ---

// csUsingName returns the namespace a using directive imports.
func csUsingName(p *fileCtx, n *sitter.Node) string {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == "qualified_name" || c.Type() == "identifier" {
			return c.Content(p.src)
		}
	}
	return ""
}

// csTypeName reduces a type/heritage node to its bare final segment: an identifier
// passes through; a qualified_name (A.B.C) yields "C"; a generic_name yields its
// underlying name. Other shapes yield "" (skipped — precision over recall).
func csTypeName(p *fileCtx, n *sitter.Node) string {
	switch n.Type() {
	case "identifier":
		return n.Content(p.src)
	case "qualified_name":
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			switch c := n.NamedChild(i); c.Type() {
			case "identifier", "generic_name":
				return csTypeName(p, c)
			}
		}
	case "generic_name":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c.Type() == "identifier" {
				return c.Content(p.src)
			}
		}
	}
	return ""
}

// csCallTarget returns the operand (final segment of the call's object, or "")
// and the method name of an invocation_expression.
func csCallTarget(p *fileCtx, call *sitter.Node) (object, name string) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return "", ""
	}
	switch fn.Type() {
	case "member_access_expression":
		if nm := fn.ChildByFieldName("name"); nm != nil {
			name = nm.Content(p.src)
		}
		if e := fn.ChildByFieldName("expression"); e != nil {
			object = csOperandName(p, e)
		}
	case "identifier":
		name = fn.Content(p.src)
	case "generic_name":
		name = csTypeName(p, fn)
	}
	return object, name
}

// csOperandName reduces a call's object expression to its final segment.
func csOperandName(p *fileCtx, n *sitter.Node) string {
	switch n.Type() {
	case "identifier":
		return n.Content(p.src)
	case "member_access_expression":
		if nm := n.ChildByFieldName("name"); nm != nil {
			return nm.Content(p.src)
		}
	case "qualified_name":
		return csTypeName(p, n)
	}
	return ""
}

// csStringLit returns the static value of a string_literal, or ("",false).
// Interpolated ($"...") and verbatim/raw strings are a distinct node type and
// never match here — they are treated as dynamic (precision over recall).
func (p *fileCtx) csStringLit(n *sitter.Node) (string, bool) {
	if n.Type() != "string_literal" {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == "string_literal_content" {
			sb.WriteString(c.Content(p.src))
		}
	}
	return sb.String(), true
}

// csArgString unwraps a C# `argument` node (or a raw expression) to its
// string-literal value.
func (p *fileCtx) csArgString(arg *sitter.Node) (string, bool) {
	if arg.Type() == "argument" {
		for i := 0; i < int(arg.NamedChildCount()); i++ {
			if s, ok := p.csStringLit(arg.NamedChild(i)); ok {
				return s, true
			}
		}
		return "", false
	}
	return p.csStringLit(arg)
}

// csFirstArgString returns the first argument of an argument_list iff it is a
// string literal.
func (p *fileCtx) csFirstArgString(args *sitter.Node) (string, bool) {
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	return p.csArgString(args.NamedChild(0))
}

// csQualifiedSegments flattens a qualified_name / identifier into its identifier
// segments in source order (A.B.C → ["A","B","C"]).
func csQualifiedSegments(p *fileCtx, n *sitter.Node) []string {
	switch n.Type() {
	case "identifier":
		return []string{n.Content(p.src)}
	case "generic_name":
		if s := csTypeName(p, n); s != "" {
			return []string{s}
		}
	case "qualified_name":
		var out []string
		for i := 0; i < int(n.NamedChildCount()); i++ {
			out = append(out, csQualifiedSegments(p, n.NamedChild(i))...)
		}
		return out
	}
	return nil
}
