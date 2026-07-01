package parse

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	tsgram "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/cpuchip/lodestar/internal/graph"
)

// tsLanguage / jsLanguage configure TypeScript and JavaScript extraction. They
// share every extractor func — the two grammars agree on the node types we touch
// (class_declaration, function_declaration, call_expression, member_expression,
// new_expression, object/pair, string). interface_declaration is TS-only; on JS it
// simply never appears. Grammar node types referenced (probed):
//
//	class_declaration      name:(type_identifier)  body:(class_body → method_definition)
//	function_declaration   name:(identifier)  body:(statement_block)
//	interface_declaration  name:(type_identifier)                                 (TS)
//	import_statement       source:(string)
//	export_statement       declaration:(...)   — unwrapped to the inner decl
func tsLanguage() Language {
	return Language{
		Name:      "typescript",
		Exts:      []string{".ts", ".tsx"},
		grammar:   tsgram.GetLanguage(),
		extract:   extractTS,
		contracts: []func(*fileCtx, *sitter.Node){extractTSHTTP, extractTSGRPC, extractTSPubSub},
	}
}

func jsLanguage() Language {
	return Language{
		Name:      "javascript",
		Exts:      []string{".js", ".mjs", ".cjs", ".jsx"},
		grammar:   javascript.GetLanguage(),
		extract:   extractTS,
		contracts: []func(*fileCtx, *sitter.Node){extractTSHTTP, extractTSGRPC, extractTSPubSub},
	}
}

func extractTS(p *fileCtx, root *sitter.Node) {
	var imports []string
	// handle a top-level node, recursing through export_statement so that
	// `export class X`, `export function f`, and `export default class Y` all reach
	// the inner declaration.
	var handle func(n *sitter.Node)
	handle = func(n *sitter.Node) {
		switch n.Type() {
		case "export_statement":
			for j := 0; j < int(n.NamedChildCount()); j++ {
				handle(n.NamedChild(j))
			}
		case "class_declaration", "abstract_class_declaration":
			p.tsClass(n)
		case "function_declaration", "generator_function_declaration":
			if name := p.fieldText(n, "name"); name != "" {
				p.addDecl(graph.KindFunction, name, nil)
			}
		case "interface_declaration":
			if name := p.fieldText(n, "name"); name != "" {
				p.addDecl(graph.KindInterface, name, nil)
			}
		case "import_statement":
			if s := tsImportSource(p, n); s != "" {
				imports = append(imports, s)
			}
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		handle(root.NamedChild(i))
	}
	if len(imports) > 0 {
		p.recordImports(imports)
	}
}

// tsClass emits the class node and its methods (Class.method).
func (p *fileCtx) tsClass(n *sitter.Node) {
	name := p.fieldText(n, "name")
	if name == "" {
		return
	}
	p.addDecl(graph.KindClass, name, nil)
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	for _, m := range namedChildrenOfType(body, "method_definition") {
		if mn := p.fieldText(m, "name"); mn != "" {
			p.addDecl(graph.KindMethod, name+"."+mn, map[string]string{"receiver": name})
		}
	}
}

// tsImportSource returns the module specifier of an import statement.
func tsImportSource(p *fileCtx, n *sitter.Node) string {
	s := n.ChildByFieldName("source")
	if s == nil {
		return ""
	}
	v, _ := p.tsStringLit(s)
	return v
}

// --- shared TS/JS tree-sitter helpers ---

// tsCallTarget returns the operand (object of a member call, or "" for a bare call)
// and the final call name (the property, or the identifier for a bare call).
func tsCallTarget(p *fileCtx, call *sitter.Node) (object, name string) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return "", ""
	}
	switch fn.Type() {
	case "member_expression":
		if pr := fn.ChildByFieldName("property"); pr != nil {
			name = pr.Content(p.src)
		}
		if o := fn.ChildByFieldName("object"); o != nil {
			object = o.Content(p.src)
		}
	case "identifier":
		name = fn.Content(p.src)
	}
	return object, name
}

// tsStringLit returns the value of a plain string node, or ("",false). Template
// literals are a distinct node type (template_string), so they never match here —
// interpolated strings are dynamic and correctly skipped (precision over recall).
func (p *fileCtx) tsStringLit(n *sitter.Node) (string, bool) {
	if n.Type() != "string" {
		return "", false
	}
	var out string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == "string_fragment" {
			out += c.Content(p.src)
		}
	}
	return out, true
}

// tsFirstArgString returns the FIRST argument iff it is a string literal.
func (p *fileCtx) tsFirstArgString(args *sitter.Node) (string, bool) {
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	return p.tsStringLit(args.NamedChild(0))
}

// tsObjectStringProp returns a string-valued property of an object literal, e.g.
// { topic: "payments" } → ("payments", true) for key "topic". The key may be a
// property_identifier (topic) or a string ("topic").
func (p *fileCtx) tsObjectStringProp(obj *sitter.Node, key string) (string, bool) {
	if obj == nil || obj.Type() != "object" {
		return "", false
	}
	for _, pr := range namedChildrenOfType(obj, "pair") {
		k := pr.ChildByFieldName("key")
		if k == nil {
			continue
		}
		keyText := k.Content(p.src)
		if k.Type() == "string" {
			keyText, _ = p.tsStringLit(k)
		}
		if keyText != key {
			continue
		}
		if v := pr.ChildByFieldName("value"); v != nil {
			if s, ok := p.tsStringLit(v); ok {
				return s, true
			}
		}
	}
	return "", false
}
