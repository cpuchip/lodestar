package parse

import (
	"net/url"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// extractGoHTTP finds HTTP producers (server routes) and consumers (client calls)
// in Go source and emits them keyed by the normalized contract key, so the resolve
// step can pair a route in one service with a call in another.
//
// Producers:
//   - net/http:  http.HandleFunc("/p", h) · mux.HandleFunc("GET /p", h) · mux.Handle("/p", h)
//   - gin/echo:  r.GET("/p", h) · e.POST("/p", h)
//   - chi:       r.Get("/p", h) · r.Post("/p", h)
//
// Consumers (net/http client):
//   - http.Get(url) · http.Post(url,...) · http.Head(url) · http.PostForm(url,...)
//   - http.NewRequest(method, url, body) · http.NewRequestWithContext(ctx, method, url, body)
//
// Only string-LITERAL paths/URLs are extracted; dynamic ones (vars, fmt.Sprintf,
// config) are skipped — precision over recall, since a false cross-edge costs more
// trust than a missed one. Path is taken statically; the resolver's key-join does
// the cross-service pairing.
func extractGoHTTP(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		operand, verb := goCallTarget(p, n)
		if verb == "" {
			return
		}
		args := p.stringArgs(n.ChildByFieldName("arguments"))

		// Consumers live on the http package.
		if operand == "http" {
			if goEmitGoHTTPClient(p, verb, args) {
				return
			}
		}
		goEmitGoHTTPRoute(p, operand, verb, args)
	})
}

// goCallTarget returns the operand ("http", a router var, or "") and the final
// selector segment (the verb: "GET", "Get", "HandleFunc", ...) of a call.
func goCallTarget(p *fileCtx, call *sitter.Node) (operand, verb string) {
	fn := call.ChildByFieldName("function")
	if fn == nil {
		return "", ""
	}
	switch fn.Type() {
	case "selector_expression":
		if f := fn.ChildByFieldName("field"); f != nil {
			verb = f.Content(p.src)
		}
		if o := fn.ChildByFieldName("operand"); o != nil {
			operand = o.Content(p.src)
		}
	case "identifier":
		verb = fn.Content(p.src)
	}
	return operand, verb
}

// goHTTPMethods maps a router-verb selector (gin/echo uppercase, chi title-case)
// to its canonical HTTP method.
var goHTTPMethods = map[string]string{
	"GET": "GET", "POST": "POST", "PUT": "PUT", "DELETE": "DELETE", "PATCH": "PATCH", "HEAD": "HEAD", "OPTIONS": "OPTIONS", "CONNECT": "CONNECT", "TRACE": "TRACE",
	"Get": "GET", "Post": "POST", "Put": "PUT", "Delete": "DELETE", "Patch": "PATCH", "Head": "HEAD", "Options": "OPTIONS", "Connect": "CONNECT", "Trace": "TRACE",
}

// goHTTPClientVerbs are the http.<Verb>(url) shorthand client calls.
var goHTTPClientVerbs = map[string]string{"Get": "GET", "Post": "POST", "Head": "HEAD", "PostForm": "POST"}

// goEmitGoHTTPClient handles http.* consumer calls; returns true if it consumed
// the call as a client (so it is not also considered a route).
func goEmitGoHTTPClient(p *fileCtx, verb string, args []string) bool {
	switch verb {
	case "Get", "Post", "Head", "PostForm":
		if len(args) == 0 {
			return true // recognized as a client call, but URL is dynamic — skip, don't fall through
		}
		p.emitHTTPClient(goHTTPClientVerbs[verb], args[0])
		return true
	case "NewRequest", "NewRequestWithContext":
		// string args are [method, url] (ctx/body are not string literals)
		if len(args) >= 2 {
			if m, ok := goHTTPMethods[strings.ToUpper(args[0])]; ok {
				p.emitHTTPClient(m, args[1])
			}
		}
		return true
	}
	return false
}

// goEmitGoHTTPRoute handles server-route producers.
func goEmitGoHTTPRoute(p *fileCtx, operand, verb string, args []string) {
	if len(args) == 0 {
		return
	}
	raw := args[0]
	var method, path string
	switch {
	case verb == "HandleFunc" || verb == "Handle":
		// net/http (and gorilla) — method may be embedded Go-1.22 style: "GET /p"
		method, path = "GET", raw
		if i := strings.IndexByte(raw, ' '); i > 0 {
			if m, ok := goHTTPMethods[strings.ToUpper(raw[:i])]; ok {
				method = m
				path = strings.TrimSpace(raw[i+1:])
			}
		}
	default:
		m, ok := goHTTPMethods[verb]
		if !ok {
			return
		}
		method, path = m, raw
	}
	// Check AFTER splitting a Go-1.22 "VERB /p" pattern: filters false positives
	// like cache.Get("key") while still accepting method-in-pattern routes.
	if !strings.HasPrefix(path, "/") {
		return
	}
	key := contracts.NormalizeHTTPKey(method, path)
	p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": method, "path": path})
}

// emitHTTPClient emits a consumer node for a client call to url (a string literal).
func (p *fileCtx) emitHTTPClient(method, rawURL string) {
	path := httpURLPath(rawURL)
	if path == "" {
		return // couldn't extract a path statically
	}
	key := contracts.NormalizeHTTPKey(method, path)
	p.addContract(graph.KindHTTPClient, key, map[string]string{"method": method, "path": path, "url": rawURL})
}

// httpURLPath pulls the path out of a client URL literal: a full URL yields its
// path component; an already-relative path passes through; anything else (a bare
// host, a template) yields "" and is skipped.
func httpURLPath(raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if u, err := url.Parse(raw); err == nil {
			if u.Path == "" {
				return "/"
			}
			return u.Path
		}
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	return ""
}
