package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// extractCSharpHTTP finds HTTP producers and consumers in C# (ASP.NET Core),
// keyed by the normalized contract key so a route pairs with any-language client.
//
// Producers:
//   - attribute routing: [HttpGet("/x")] / [HttpPost] / ... on a controller
//     method, prefixed by a class-level [Route("/base")]
//   - minimal API: app.MapGet("/x", ...) / MapPost / MapPut / MapDelete / MapPatch
//
// Consumers (HttpClient, string-literal URL only):
//   - client.GetAsync/PostAsync/PutAsync/DeleteAsync/PatchAsync(url)
//   - GetStringAsync / GetFromJsonAsync (GET) · PostAsJsonAsync (POST)
//
// Only string-LITERAL paths/URLs are extracted. Attribute route tokens such as
// [controller]/[action] are NOT expanded (they need the class/method name and a
// convention map); a literal-only base keeps precision. The httpURLPath gate on
// consumers rejects any non-URL first arg.
func extractCSharpHTTP(p *fileCtx, root *sitter.Node) {
	// Producers: descend types carrying the class-level [Route] base path.
	var handleType func(n *sitter.Node, base string)
	handleType = func(n *sitter.Node, base string) {
		clsBase := base + p.csClassRoute(n)
		body := n.ChildByFieldName("body")
		if body == nil {
			return
		}
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch {
			case m.Type() == "method_declaration":
				p.csHTTPRoute(m, clsBase)
			case isCSharpTypeDecl(m.Type()):
				handleType(m, clsBase)
			}
		}
	}
	var descend func(n *sitter.Node)
	descend = func(n *sitter.Node) {
		switch {
		case isCSharpTypeDecl(n.Type()):
			handleType(n, "")
		case n.Type() == "namespace_declaration" || n.Type() == "file_scoped_namespace_declaration" ||
			n.Type() == "declaration_list" || n.Type() == "compilation_unit":
			for i := 0; i < int(n.NamedChildCount()); i++ {
				descend(n.NamedChild(i))
			}
		}
	}
	descend(root)

	// Consumers + minimal-API producers: invocations anywhere in the file.
	walk(root, func(n *sitter.Node) {
		if n.Type() != "invocation_expression" {
			return
		}
		_, verb := csCallTarget(p, n)
		args := n.ChildByFieldName("arguments")
		if m, ok := csMinimalAPIVerbs[verb]; ok {
			if path, ok := p.csFirstArgString(args); ok && strings.HasPrefix(path, "/") {
				key := contracts.NormalizeHTTPKey(m, path)
				p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": m, "path": path})
			}
			return
		}
		if m, ok := csHTTPClientVerbs[verb]; ok {
			if args != nil && args.NamedChildCount() > 0 {
				p.emitCSClientURL(m, args.NamedChild(0))
			}
		}
	})
}

// emitCSClientURL emits a consumer for an HttpClient call argument: a plain string
// literal (existing path), or a concatenation / interpolated string
// (baseUrl + "/api/v1/svc/things/" + id · $"{baseUrl}/api/v1/svc/things/{id}")
// reconstructed to a templated path. The C# `argument` wrapper is unwrapped first;
// csStringLit fails on an interpolated string, so reconstruction picks it up.
func (p *fileCtx) emitCSClientURL(method string, arg *sitter.Node) {
	node := arg
	if node.Type() == "argument" && node.NamedChildCount() > 0 {
		node = node.NamedChild(0)
	}
	s, ok := p.csStringLit(node)
	p.emitHTTPClientFromNode(method, node, s, ok)
}

// joinAspNetPath joins an ASP.NET controller [Route] base with a method route
// template. A method template beginning with "/" is ABSOLUTE and overrides the
// controller prefix (ASP.NET routing rule); otherwise it is relative and joined.
func joinAspNetPath(base, path string) string {
	if strings.HasPrefix(path, "/") {
		return path // absolute override — controller route ignored
	}
	return joinRoutePath(base, path)
}

// csHTTPAttrMethods maps an ASP.NET HTTP-verb attribute to its method.
var csHTTPAttrMethods = map[string]string{
	"HttpGet": "GET", "HttpPost": "POST", "HttpPut": "PUT",
	"HttpDelete": "DELETE", "HttpPatch": "PATCH", "HttpHead": "HEAD", "HttpOptions": "OPTIONS",
}

// csMinimalAPIVerbs maps a minimal-API mapping call to its method.
var csMinimalAPIVerbs = map[string]string{
	"MapGet": "GET", "MapPost": "POST", "MapPut": "PUT", "MapDelete": "DELETE", "MapPatch": "PATCH",
}

// csHTTPClientVerbs maps an HttpClient method to its HTTP method.
var csHTTPClientVerbs = map[string]string{
	"GetAsync": "GET", "PostAsync": "POST", "PutAsync": "PUT", "DeleteAsync": "DELETE",
	"PatchAsync": "PATCH", "GetStringAsync": "GET", "GetByteArrayAsync": "GET",
	"GetStreamAsync": "GET", "GetFromJsonAsync": "GET", "PostAsJsonAsync": "POST",
}

// csClassRoute returns the class-level [Route("/base")] path, or "".
func (p *fileCtx) csClassRoute(n *sitter.Node) string {
	for _, at := range csAttributes(n) {
		if p.csAttrName(at) != "Route" {
			continue
		}
		if path, ok := p.csAttrFirstString(at); ok && strings.HasPrefix(path, "/") {
			return path
		}
	}
	return ""
}

// csHTTPRoute emits a producer for a controller method with an HTTP-verb attribute.
func (p *fileCtx) csHTTPRoute(m *sitter.Node, base string) {
	method, path := "", ""
	for _, at := range csAttributes(m) {
		name := p.csAttrName(at)
		if hm, ok := csHTTPAttrMethods[name]; ok {
			method = hm
			if pth, ok := p.csAttrFirstString(at); ok {
				path = pth
			}
		} else if name == "Route" && path == "" {
			if pth, ok := p.csAttrFirstString(at); ok {
				path = pth
			}
		}
	}
	if method == "" {
		return // no HTTP-verb attribute → not an endpoint
	}
	full := joinAspNetPath(base, path)
	if !strings.HasPrefix(full, "/") {
		return
	}
	key := contracts.NormalizeHTTPKey(method, full)
	p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": method, "path": full})
}

// csAttributes returns the attribute nodes attached to a declaration (each
// attribute_list child holds one or more attribute nodes).
func csAttributes(n *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	for _, al := range namedChildrenOfType(n, "attribute_list") {
		out = append(out, namedChildrenOfType(al, "attribute")...)
	}
	return out
}

// csAttrName returns an attribute's name (its identifier / qualified_name head).
func (p *fileCtx) csAttrName(at *sitter.Node) string {
	if nm := at.ChildByFieldName("name"); nm != nil {
		return csTypeName(p, nm)
	}
	for i := 0; i < int(at.NamedChildCount()); i++ {
		switch c := at.NamedChild(i); c.Type() {
		case "identifier":
			return c.Content(p.src)
		case "qualified_name":
			return csTypeName(p, c)
		}
	}
	return ""
}

// csAttrFirstString returns the first POSITIONAL string argument of an attribute
// ([HttpGet("/x")] → "/x"), or ("",false) for a named-only arg ([HttpGet(Name=..)]).
func (p *fileCtx) csAttrFirstString(at *sitter.Node) (string, bool) {
	al := namedChildrenOfType(at, "attribute_argument_list")
	if len(al) == 0 {
		return "", false
	}
	args := namedChildrenOfType(al[0], "attribute_argument")
	if len(args) == 0 {
		return "", false
	}
	first := args[0]
	if first.NamedChildCount() == 0 {
		return "", false
	}
	return p.csStringLit(first.NamedChild(0))
}
