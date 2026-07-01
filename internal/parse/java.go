package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"

	"github.com/cpuchip/lodestar/internal/graph"
)

// javaLanguage configures Java extraction. Grammar node types referenced (probed
// against tree-sitter-java):
//
//	class_declaration      name:(identifier)  superclass:(superclass)  interfaces:(super_interfaces)  body:(class_body)
//	interface_declaration  name:(identifier)  body:(interface_body)
//	enum_declaration        name:(identifier)  body:(enum_body)
//	record_declaration      name:(identifier)  body:(class_body)
//	method_declaration      name:(identifier)  body:(block)  modifiers:(modifiers → annotation*)
//	method_invocation       object:(...)  name:(identifier)  arguments:(argument_list)
//	import_declaration      → (scoped_identifier|identifier) [asterisk]
//	string_literal          → (string_fragment)*                (Java has no string interpolation)
//
// Unlike the Go/Python/TS passes (top-level only), Java nests types heavily —
// an inner impl class (e.g. AdServiceImpl extends AdServiceGrpc.AdServiceImplBase)
// is idiomatic and load-bearing — so type declarations are walked recursively into
// class bodies. Only type declarations and their direct methods become nodes;
// statements and locals are implementation detail.
func javaLanguage() Language {
	return Language{
		Name:      "java",
		Exts:      []string{".java"},
		grammar:   java.GetLanguage(),
		extract:   extractJava,
		contracts: []func(*fileCtx, *sitter.Node){extractJavaHTTP, extractJavaGRPC, extractJavaPubSub, extractJavaConfig, extractSQLTables},
	}
}

// javaTypeDeclTypes are the Java declaration node types that become a type node.
func isJavaTypeDecl(t string) bool {
	switch t {
	case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
		return true
	}
	return false
}

func extractJava(p *fileCtx, root *sitter.Node) {
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
		p.javaHeritage(classID, n)
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
					p.recordJavaCalls(id, m)
				}
			case isJavaTypeDecl(m.Type()):
				handleType(m) // nested type
			}
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch {
		case isJavaTypeDecl(n.Type()):
			handleType(n)
		case n.Type() == "import_declaration":
			if s := javaImport(p, n); s != "" {
				imports = append(imports, s)
			}
		}
	}
	if len(imports) > 0 {
		p.recordImports(imports)
	}
}

// javaHeritage records inherits (superclass) and implements (super_interfaces)
// refs for the deep call graph. Each target is reduced to a bare final segment;
// resolveRefs pairs it only to a unique in-world class/interface (relAcceptsKind
// gates inherits→class, implements→interface, so a mislabel yields no edge).
func (p *fileCtx) javaHeritage(classID string, n *sitter.Node) {
	if sc := n.ChildByFieldName("superclass"); sc != nil {
		for i := 0; i < int(sc.NamedChildCount()); i++ {
			if base := javaTypeName(p, sc.NamedChild(i)); base != "" {
				p.addRef(classID, base, graph.RelInherits)
				break
			}
		}
	}
	if si := n.ChildByFieldName("interfaces"); si != nil {
		for _, tl := range namedChildrenOfType(si, "type_list") {
			for j := 0; j < int(tl.NamedChildCount()); j++ {
				if iface := javaTypeName(p, tl.NamedChild(j)); iface != "" {
					p.addRef(classID, iface, graph.RelImplements)
				}
			}
		}
	}
}

// recordJavaCalls records a pending calls ref for every method_invocation in a
// method body. The callee is the bare method name (`s.setup()` -> "setup",
// `Foo.bar()` -> "bar"); the object operand is dropped — V1 resolves on the bare
// name, unique-match only, so a stdlib callee yields no edge.
func (p *fileCtx) recordJavaCalls(declID string, decl *sitter.Node) {
	p.recordCalls(declID, decl.ChildByFieldName("body"), "method_invocation", func(n *sitter.Node) string {
		if nm := n.ChildByFieldName("name"); nm != nil {
			return nm.Content(p.src)
		}
		return ""
	})
}

// --- shared Java tree-sitter helpers ---

// javaImport returns the dotted module path of an import declaration:
//
//	import io.grpc.Server;   → "io.grpc.Server"
//	import io.grpc.stub.*;    → "io.grpc.stub"   (the .* is dropped)
//	import static a.b.C.d;    → "a.b.C.d"
func javaImport(p *fileCtx, decl *sitter.Node) string {
	for i := 0; i < int(decl.NamedChildCount()); i++ {
		c := decl.NamedChild(i)
		if c.Type() == "scoped_identifier" || c.Type() == "identifier" {
			return c.Content(p.src)
		}
	}
	return ""
}

// javaTypeName reduces a heritage/type node to its bare final segment: a
// type_identifier passes through; a scoped_type_identifier yields its last
// segment (a.b.Base -> "Base"); a generic_type yields its underlying name.
// Anything else yields "" (skipped — precision over recall).
func javaTypeName(p *fileCtx, n *sitter.Node) string {
	switch n.Type() {
	case "type_identifier", "identifier":
		return n.Content(p.src)
	case "scoped_type_identifier":
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			if c := n.NamedChild(i); c.Type() == "type_identifier" {
				return c.Content(p.src)
			}
		}
	case "generic_type":
		if n.NamedChildCount() > 0 {
			return javaTypeName(p, n.NamedChild(0))
		}
	}
	return ""
}

// javaCallTarget returns the operand (final segment of the call's object, or "")
// and the method name of a method_invocation. Mirrors goCallTarget/pyCallTarget.
func javaCallTarget(p *fileCtx, call *sitter.Node) (object, name string) {
	if nm := call.ChildByFieldName("name"); nm != nil {
		name = nm.Content(p.src)
	}
	if o := call.ChildByFieldName("object"); o != nil {
		object = javaOperandName(p, o)
	}
	return object, name
}

// javaOperandName reduces a call's object expression to its final segment:
// an identifier passes through; a field_access (a.b.C) yields its last segment.
func javaOperandName(p *fileCtx, n *sitter.Node) string {
	switch n.Type() {
	case "identifier", "type_identifier":
		return n.Content(p.src)
	case "field_access":
		if f := n.ChildByFieldName("field"); f != nil {
			return f.Content(p.src)
		}
		// fallback: last named child
		if n.NamedChildCount() > 0 {
			return javaOperandName(p, n.NamedChild(int(n.NamedChildCount())-1))
		}
	case "scoped_type_identifier":
		return javaTypeName(p, n)
	}
	return ""
}

// javaStringLit returns the static value of a string_literal node, or ("",false).
// Java string literals carry no interpolation, so the value is always static.
func (p *fileCtx) javaStringLit(n *sitter.Node) (string, bool) {
	if n.Type() != "string_literal" {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == "string_fragment" {
			sb.WriteString(c.Content(p.src))
		}
	}
	return sb.String(), true // an empty "" literal (no children) is a valid empty string
}

// javaFirstArgString returns the FIRST argument of an argument_list iff it is a
// string literal.
func (p *fileCtx) javaFirstArgString(argList *sitter.Node) (string, bool) {
	if argList == nil || argList.NamedChildCount() == 0 {
		return "", false
	}
	return p.javaStringLit(argList.NamedChild(0))
}

// --- Java annotation helpers (shared by the HTTP and pub/sub passes) ---

// javaAnnotationName returns an annotation's name (marker_annotation or
// annotation both hold it as the first identifier child).
func (p *fileCtx) javaAnnotationName(n *sitter.Node) string {
	if nm := n.ChildByFieldName("name"); nm != nil {
		return nm.Content(p.src)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == "identifier" {
			return c.Content(p.src)
		}
	}
	return ""
}

// javaAnnotations returns the annotation/marker_annotation nodes attached to a
// type or method declaration (they live in its `modifiers` child).
func javaAnnotations(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() != "modifiers" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			t := c.NamedChild(j).Type()
			if t == "annotation" || t == "marker_annotation" {
				out = append(out, c.NamedChild(j))
			}
		}
	}
	return out
}

// javaAnnotationValue returns the string value of an annotation argument: the
// bare positional string (@GetMapping("/x")) when key=="value", or the string in
// an element_value_pair whose name matches key (@RequestMapping(value="/x")).
func (p *fileCtx) javaAnnotationValue(ann *sitter.Node, key string) (string, bool) {
	al := ann.ChildByFieldName("arguments")
	if al == nil {
		for i := 0; i < int(ann.NamedChildCount()); i++ {
			if c := ann.NamedChild(i); c.Type() == "annotation_argument_list" {
				al = c
				break
			}
		}
	}
	if al == nil {
		return "", false
	}
	// bare positional string literal → the default "value"
	if key == "value" {
		for i := 0; i < int(al.NamedChildCount()); i++ {
			if s, ok := p.javaStringLit(al.NamedChild(i)); ok {
				return s, true
			}
		}
	}
	for _, pair := range namedChildrenOfType(al, "element_value_pair") {
		if pair.NamedChildCount() < 2 {
			continue
		}
		if pair.NamedChild(0).Content(p.src) != key {
			continue
		}
		if s, ok := p.javaStringLit(pair.NamedChild(1)); ok {
			return s, true
		}
	}
	return "", false
}

// javaAnnotationValues returns all string values for an annotation key, handling
// both a single string and a { "a", "b" } array (e.g. @KafkaListener(topics=...)).
func (p *fileCtx) javaAnnotationValues(ann *sitter.Node, key string) []string {
	al := ann.ChildByFieldName("arguments")
	if al == nil {
		for i := 0; i < int(ann.NamedChildCount()); i++ {
			if c := ann.NamedChild(i); c.Type() == "annotation_argument_list" {
				al = c
				break
			}
		}
	}
	if al == nil {
		return nil
	}
	var out []string
	collect := func(v *sitter.Node) {
		if s, ok := p.javaStringLit(v); ok {
			out = append(out, s)
			return
		}
		if v.Type() == "element_value_array_initializer" || v.Type() == "array_initializer" {
			for i := 0; i < int(v.NamedChildCount()); i++ {
				if s, ok := p.javaStringLit(v.NamedChild(i)); ok {
					out = append(out, s)
				}
			}
		}
	}
	for _, pair := range namedChildrenOfType(al, "element_value_pair") {
		if pair.NamedChildCount() >= 2 && pair.NamedChild(0).Content(p.src) == key {
			collect(pair.NamedChild(1))
		}
	}
	return out
}
