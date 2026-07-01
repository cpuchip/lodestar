package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"

	"github.com/cpuchip/lodestar/internal/graph"
)

// pythonLanguage configures Python extraction. Grammar node types referenced
// (probed against tree-sitter-python):
//
//	class_definition       name:(identifier)  body:(block)
//	function_definition    name:(identifier)  parameters:(parameters)  body:(block)
//	decorated_definition   (decorator ...) definition:(function_definition|class_definition)
//	import_statement       name:(dotted_name|aliased_import)
//	import_from_statement  module_name:(dotted_name)  name:(dotted_name)...
//
// Only module-level and class-body declarations are walked — nested defs are
// implementation detail, not the service's public skeleton, matching the Go pass.
func pythonLanguage() Language {
	return Language{
		Name:      "python",
		Exts:      []string{".py"},
		grammar:   python.GetLanguage(),
		extract:   extractPython,
		contracts: []func(*fileCtx, *sitter.Node){extractPythonHTTP, extractPythonGRPC, extractPythonPubSub},
	}
}

func extractPython(p *fileCtx, root *sitter.Node) {
	var imports []string
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "function_definition":
			if name := p.fieldText(n, "name"); name != "" {
				p.addDecl(graph.KindFunction, name, nil)
			}
		case "class_definition":
			p.pyClass(n)
		case "decorated_definition":
			// A decorated module-level def is still a module-level function/class;
			// the decorator itself (e.g. @app.get) is picked up by the HTTP pass.
			def := n.ChildByFieldName("definition")
			if def == nil {
				continue
			}
			switch def.Type() {
			case "function_definition":
				if name := p.fieldText(def, "name"); name != "" {
					p.addDecl(graph.KindFunction, name, nil)
				}
			case "class_definition":
				p.pyClass(def)
			}
		case "import_statement", "import_from_statement":
			imports = append(imports, pyImports(p, n)...)
		}
	}
	if len(imports) > 0 {
		p.recordImports(imports)
	}
}

// pyClass emits the class node and its methods (Class.method), unwrapping any
// decorated methods (e.g. @property) to their inner function_definition.
func (p *fileCtx) pyClass(n *sitter.Node) {
	name := p.fieldText(n, "name")
	if name == "" {
		return
	}
	p.addDecl(graph.KindClass, name, nil)
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		m := body.NamedChild(i)
		if m.Type() == "decorated_definition" {
			m = m.ChildByFieldName("definition")
			if m == nil {
				continue
			}
		}
		if m.Type() != "function_definition" {
			continue
		}
		if mn := p.fieldText(m, "name"); mn != "" {
			p.addDecl(graph.KindMethod, name+"."+mn, map[string]string{"receiver": name})
		}
	}
}

// recordImports stores the file's import module names as file-node metadata (not
// as fake nodes) — an import target is another world's package, and the contract
// layer is what links across worlds. Shared by the Python and TS structural passes.
func (p *fileCtx) recordImports(imports []string) {
	for i := range p.g.Nodes {
		if p.g.Nodes[i].ID == p.fileID {
			if p.g.Nodes[i].Metadata == nil {
				p.g.Nodes[i].Metadata = map[string]string{}
			}
			p.g.Nodes[i].Metadata["imports"] = strings.Join(imports, " ")
			return
		}
	}
}

// pyImports returns the module name(s) an import statement pulls in:
//
//	import os              → "os"
//	import a.b as c        → "a.b"
//	from a.b import c, d   → "a.b"   (the module, not the imported symbols)
func pyImports(p *fileCtx, n *sitter.Node) []string {
	if n.Type() == "import_from_statement" {
		if m := n.ChildByFieldName("module_name"); m != nil {
			return []string{m.Content(p.src)}
		}
		return nil
	}
	var out []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "dotted_name":
			out = append(out, c.Content(p.src))
		case "aliased_import":
			if dn := c.ChildByFieldName("name"); dn != nil {
				out = append(out, dn.Content(p.src))
			}
		}
	}
	return out
}

// --- shared Python tree-sitter helpers ---

// pyCallTarget returns the operand (the object of a method call, or "" for a bare
// call) and the final call name (the attribute, or the identifier for a bare call).
// Mirrors goCallTarget for Python's attribute/call grammar.
func pyCallTarget(p *fileCtx, call *sitter.Node) (object, name string) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return "", ""
	}
	switch fn.Type() {
	case "attribute":
		if a := fn.ChildByFieldName("attribute"); a != nil {
			name = a.Content(p.src)
		}
		if o := fn.ChildByFieldName("object"); o != nil {
			object = o.Content(p.src)
		}
	case "identifier":
		name = fn.Content(p.src)
	}
	return object, name
}

// pyStringLit returns the value of a Python string node, or ("",false) if n is not
// a string. An f-string / interpolated string yields ("",false) — its value is
// dynamic, and precision beats recall (a false cross-edge costs more than a miss).
func (p *fileCtx) pyStringLit(n *sitter.Node) (string, bool) {
	if n.Type() != "string" {
		return "", false
	}
	var sb strings.Builder
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "interpolation":
			return "", false // f-string → dynamic
		case "string_content":
			sb.WriteString(c.Content(p.src))
		}
	}
	return sb.String(), true
}

// pyFirstArgString returns the FIRST positional argument iff it is a string literal
// (mirrors firstArgString for Python's argument_list, where a leading keyword or
// non-literal arg means the subject/path is not statically known).
func (p *fileCtx) pyFirstArgString(argList *sitter.Node) (string, bool) {
	if argList == nil || argList.NamedChildCount() == 0 {
		return "", false
	}
	return p.pyStringLit(argList.NamedChild(0))
}

// pyStringArgs returns the string-literal children of a node in order (works for
// an argument_list or a list — used for positional args and for list literals like
// methods=["POST"] and consumer.subscribe(["a","b"])).
func (p *fileCtx) pyStringArgs(n *sitter.Node) []string {
	var out []string
	if n == nil {
		return out
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if s, ok := p.pyStringLit(n.NamedChild(i)); ok {
			out = append(out, s)
		}
	}
	return out
}

// pyKeywordListArg returns the string-literal elements of a keyword argument whose
// value is a list, e.g. methods=["POST","PUT"] → ["POST","PUT"].
func (p *fileCtx) pyKeywordListArg(argList *sitter.Node, key string) []string {
	if argList == nil {
		return nil
	}
	for _, kw := range namedChildrenOfType(argList, "keyword_argument") {
		name := kw.ChildByFieldName("name")
		if name == nil || name.Content(p.src) != key {
			continue
		}
		if v := kw.ChildByFieldName("value"); v != nil && v.Type() == "list" {
			return p.pyStringArgs(v)
		}
	}
	return nil
}
