package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// extractTSHTTP finds HTTP producers and consumers in TS/JS, keyed by the
// normalized contract key so a TS route/call pairs with a peer in any language.
//
// Producers (Express-style routers):
//   - app.get("/p", h) · router.post("/p", h) · app.put/delete/patch/all
//
// Consumers (string-literal URL only):
//   - fetch("url") · axios("url") · axios.get/post(...) · http.get(...)
//
// A route producer and a client consumer are told apart by the operand: axios/http
// calls are consumers; any other member verb with a "/"-leading literal is a route.
// Only string-LITERAL paths/URLs are extracted (template literals and vars skipped).
func extractTSHTTP(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		object, verb := tsCallTarget(p, n)
		args := n.ChildByFieldName("arguments")

		// Consumers: bare fetch()/axios(), or axios.<verb>()/http.<verb>().
		switch {
		case object == "" && (verb == "fetch" || verb == "axios"):
			p.tsEmitHTTPClient("GET", args)
			return
		case object == "axios" || object == "http":
			if m, ok := tsHTTPClientVerbs[verb]; ok {
				p.tsEmitHTTPClient(m, args)
			}
			return
		}

		// Producers: router verb + a "/"-leading literal path.
		m, ok := tsHTTPMethods[verb]
		if !ok {
			return
		}
		path, ok := p.tsFirstArgString(args)
		if !ok || !strings.HasPrefix(path, "/") {
			return
		}
		key := contracts.NormalizeHTTPKey(m, path)
		p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": m, "path": path})
	})
}

// tsHTTPMethods maps an Express router verb to its canonical method. "all"
// registers every method; it is emitted as ALL and simply never pairs with a
// single-method client call, which is the safe outcome.
var tsHTTPMethods = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "head": "HEAD", "options": "OPTIONS", "all": "ALL",
}

// tsHTTPClientVerbs maps an axios/http client verb to its method.
var tsHTTPClientVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "head": "HEAD",
}

// tsEmitHTTPClient emits a consumer node for a client call whose first arg is a URL:
// a plain string literal (existing path via httpURLPath), or a concatenation /
// template string (base + "/users/" + id · `${base}/users/${id}`) reconstructed to a
// templated path. tsStringLit fails on a template_string, so the reconstruction
// fallback picks it up. fetch/axios default to GET; a method= option is not read.
func (p *fileCtx) tsEmitHTTPClient(method string, args *sitter.Node) {
	if args == nil || args.NamedChildCount() == 0 {
		return
	}
	urlNode := args.NamedChild(0)
	s, ok := p.tsStringLit(urlNode)
	p.emitHTTPClientFromNode(method, urlNode, s, ok)
}
