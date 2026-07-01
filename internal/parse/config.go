package parse

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// Config/env extraction: two services that read the SAME environment variable are
// symmetrically coupled — they share configuration (a connection string, a broker
// address, a feature flag). Unlike HTTP/gRPC/pub-sub these have no producer and no
// consumer; every reader is a peer. The extractors below emit one KindConfigKey
// node per env var read, keyed by the var name AS WRITTEN (env vars are
// case-sensitive and conventionally upper-snake), and the resolve step's symmetric
// mode groups them by name across worlds. The heavy lifting on precision lives at
// resolve time (isConfigNoise denylist + the maxWorlds cap) — a var read in one
// world, or a ubiquitous infra var like PORT, produces no coupling.
//
// Only STATIC string-literal var names are extracted; a dynamic name (a variable,
// a concatenation) is skipped — precision over recall, matching the contract
// extractors.

// extractGoConfig finds env reads in Go: os.Getenv("X") / os.LookupEnv("X").
func extractGoConfig(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		operand, verb := goCallTarget(p, n)
		if operand != "os" {
			return
		}
		if verb != "Getenv" && verb != "LookupEnv" {
			return
		}
		if name, ok := p.firstArgString(n.ChildByFieldName("arguments")); ok && name != "" {
			p.addContract(graph.KindConfigKey, name, map[string]string{"env": name})
		}
	})
}

// extractPythonConfig finds env reads in Python:
//
//	os.environ["X"]      → subscript on os.environ
//	os.environ.get("X")  → call, object os.environ, method get
//	os.getenv("X")       → call, object os, method getenv
func extractPythonConfig(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "subscript":
			// os.environ["X"] — first named child is the value (os.environ),
			// the key is a string child.
			if n.NamedChildCount() == 0 {
				return
			}
			if n.NamedChild(0).Content(p.src) != "os.environ" {
				return
			}
			for i := 1; i < int(n.NamedChildCount()); i++ {
				if name, ok := p.pyStringLit(n.NamedChild(i)); ok && name != "" {
					p.addContract(graph.KindConfigKey, name, map[string]string{"env": name})
					return
				}
			}
		case "call":
			object, name := pyCallTarget(p, n)
			args := n.ChildByFieldName("arguments")
			isEnvCall := (object == "os.environ" && name == "get") || (object == "os" && name == "getenv")
			if !isEnvCall {
				return
			}
			if v, ok := p.pyFirstArgString(args); ok && v != "" {
				p.addContract(graph.KindConfigKey, v, map[string]string{"env": v})
			}
		}
	})
}

// extractJavaConfig finds env reads in Java: System.getenv("X").
func extractJavaConfig(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "method_invocation" {
			return
		}
		object, name := javaCallTarget(p, n)
		if object != "System" || name != "getenv" {
			return
		}
		if v, ok := p.javaFirstArgString(n.ChildByFieldName("arguments")); ok && v != "" {
			p.addContract(graph.KindConfigKey, v, map[string]string{"env": v})
		}
	})
}

// extractCSharpConfig finds config reads in C#:
//
//	Environment.GetEnvironmentVariable("X")  → call, object Environment
//	Configuration["X"]                        → element access on a Configuration ref
func extractCSharpConfig(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "invocation_expression":
			object, name := csCallTarget(p, n)
			if object != "Environment" || name != "GetEnvironmentVariable" {
				return
			}
			if v, ok := p.csFirstArgString(n.ChildByFieldName("arguments")); ok && v != "" {
				p.addContract(graph.KindConfigKey, v, map[string]string{"env": v})
			}
		case "element_access_expression":
			expr := n.ChildByFieldName("expression")
			if expr == nil {
				return
			}
			// The IConfiguration is conventionally named Configuration or
			// _configuration (field). Its final segment gates the read.
			switch csOperandName(p, expr) {
			case "Configuration", "_configuration":
			default:
				return
			}
			if v, ok := p.csFirstArgString(n.ChildByFieldName("subscript")); ok && v != "" {
				p.addContract(graph.KindConfigKey, v, map[string]string{"env": v})
			}
		}
	})
}

// extractTSConfig finds env reads in TS/JS: process.env.X and process.env["X"].
// Both hang off the process.env member expression; the var name is the trailing
// property (member) or the string subscript. The `process.env` member node itself
// (object "process", property "env") is not a read and is naturally skipped because
// its object is not "process.env".
func extractTSConfig(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "member_expression":
			obj := n.ChildByFieldName("object")
			if obj == nil || obj.Content(p.src) != "process.env" {
				return
			}
			if prop := n.ChildByFieldName("property"); prop != nil {
				if name := prop.Content(p.src); name != "" {
					p.addContract(graph.KindConfigKey, name, map[string]string{"env": name})
				}
			}
		case "subscript_expression":
			obj := n.ChildByFieldName("object")
			if obj == nil || obj.Content(p.src) != "process.env" {
				return
			}
			idx := n.ChildByFieldName("index")
			if idx == nil {
				// fall back to the first string child if the field is unnamed
				for i := 0; i < int(n.NamedChildCount()); i++ {
					if c := n.NamedChild(i); c.Type() == "string" {
						idx = c
						break
					}
				}
			}
			if idx == nil {
				return
			}
			if name, ok := p.tsStringLit(idx); ok && name != "" {
				p.addContract(graph.KindConfigKey, name, map[string]string{"env": name})
			}
		}
	})
}
