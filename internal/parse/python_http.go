package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// extractPythonHTTP finds HTTP producers and consumers in Python, keyed by the
// normalized contract key so a Python route/call pairs with a Go (or any) peer.
//
// Producers (decorators):
//   - FastAPI/router:  @app.get("/p") · @router.post("/p") · @app.put/delete/patch
//   - Flask:           @app.route("/p", methods=["POST"])  (default GET when absent)
//
// Consumers (client calls, string-literal URL only):
//   - requests.get/post/... · httpx.get/... · session.get/... · client.get/...
//
// Only string-LITERAL paths/URLs are extracted; f-strings and variables are
// skipped (precision over recall). The URL→path reduction reuses httpURLPath, the
// same helper the Go client extractor uses, so both sides normalize identically.
func extractPythonHTTP(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "decorator":
			pyHTTPRoute(p, n)
		case "call":
			pyHTTPClient(p, n)
		}
	})
}

// pyHTTPMethods maps a lowercase decorator/verb selector to its canonical method.
var pyHTTPMethods = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "head": "HEAD", "options": "OPTIONS",
}

// pyHTTPClientObjects are the well-known HTTP client roots. Restricting consumers
// to these (plus a string-literal URL) keeps dict.get / cache.get out of the graph.
var pyHTTPClientObjects = map[string]bool{
	"requests": true, "httpx": true, "session": true, "client": true,
}

// pyHTTPRoute emits a producer from a route decorator (@app.get("/p") / @app.route).
func pyHTTPRoute(p *fileCtx, decorator *sitter.Node) {
	var call *sitter.Node
	for i := 0; i < int(decorator.NamedChildCount()); i++ {
		if decorator.NamedChild(i).Type() == "call" {
			call = decorator.NamedChild(i)
			break
		}
	}
	if call == nil {
		return
	}
	_, verb := pyCallTarget(p, call)
	if verb == "" {
		return
	}
	args := call.ChildByFieldName("arguments")
	path, ok := p.pyFirstArgString(args)
	if !ok || !strings.HasPrefix(path, "/") {
		return
	}
	if verb == "route" {
		// Flask: method(s) live in a methods=[...] kwarg; default GET.
		methods := p.pyKeywordListArg(args, "methods")
		if len(methods) == 0 {
			methods = []string{"GET"}
		}
		for _, m := range methods {
			p.emitPyRoute(strings.ToUpper(m), path)
		}
		return
	}
	if m, ok := pyHTTPMethods[verb]; ok {
		p.emitPyRoute(m, path)
	}
}

func (p *fileCtx) emitPyRoute(method, path string) {
	key := contracts.NormalizeHTTPKey(method, path)
	p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": method, "path": path})
}

// pyHTTPClient emits a consumer from a client call (requests.get("url"), ...).
func pyHTTPClient(p *fileCtx, call *sitter.Node) {
	object, verb := pyCallTarget(p, call)
	if !pyHTTPClientObjects[object] {
		return
	}
	method, ok := pyHTTPMethods[verb]
	if !ok {
		return
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return
	}
	// First arg is the URL: a plain string literal (existing path via httpURLPath),
	// or a concatenation / f-string (base + "/users/" + uid · f"{base}/users/{uid}")
	// reconstructed to a templated path. pyStringLit fails on an f-string, so the
	// reconstruction fallback picks it up.
	urlNode := args.NamedChild(0)
	s, ok := p.pyStringLit(urlNode)
	p.emitHTTPClientFromNode(method, urlNode, s, ok)
}
