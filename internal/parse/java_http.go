package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// extractJavaHTTP finds HTTP producers and consumers in Java (Spring MVC + Spring
// RestTemplate), keyed by the normalized contract key so a Spring route pairs with
// a client call in any language.
//
// Producers (Spring annotations):
//   - @GetMapping("/x") · @PostMapping · @PutMapping · @DeleteMapping · @PatchMapping
//   - @RequestMapping(value="/x", method=RequestMethod.GET)   (default GET)
//   - class-level @RequestMapping("/base") is prefixed onto each method's path,
//     so @RequestMapping("/users") on the controller + @GetMapping("/{id}") on the
//     method normalizes to GET /users/{} — matching a Go http.Get(".../users/1").
//
// Consumers (Spring RestTemplate, string-literal URL only):
//   - getForObject/getForEntity → GET · postForObject/postForEntity → POST
//   - put → PUT · delete → DELETE · exchange(url, HttpMethod.X, ...) → X
//
// DEFERRED: WebClient / java.net.http HttpClient fluent chains
// (webClient.get().uri("/x").retrieve()) — the method and the path live on
// separate chained calls, so associating them statically is unreliable; skipping
// keeps precision (a false cross-edge costs more than a miss). Only string-LITERAL
// paths/URLs are extracted; dynamic ones are skipped.
func extractJavaHTTP(p *fileCtx, root *sitter.Node) {
	// Producers: descend types carrying the class-level @RequestMapping base path.
	var handleType func(n *sitter.Node, base string)
	handleType = func(n *sitter.Node, base string) {
		clsBase := base + p.javaClassBasePath(n)
		body := n.ChildByFieldName("body")
		if body == nil {
			return
		}
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch {
			case m.Type() == "method_declaration":
				p.javaHTTPRoute(m, clsBase)
			case isJavaTypeDecl(m.Type()):
				handleType(m, clsBase)
			}
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		if n := root.NamedChild(i); isJavaTypeDecl(n.Type()) {
			handleType(n, "")
		}
	}
	// Consumers: RestTemplate calls anywhere in the file.
	walk(root, func(n *sitter.Node) {
		if n.Type() == "method_invocation" {
			p.javaHTTPClient(n)
		}
	})
}

// javaMappingMethods maps a Spring mapping annotation to its HTTP method.
var javaMappingMethods = map[string]string{
	"GetMapping": "GET", "PostMapping": "POST", "PutMapping": "PUT",
	"DeleteMapping": "DELETE", "PatchMapping": "PATCH",
}

// javaClassBasePath returns the class-level @RequestMapping base path, or "".
func (p *fileCtx) javaClassBasePath(n *sitter.Node) string {
	for _, ann := range javaAnnotations(n) {
		if p.javaAnnotationName(ann) != "RequestMapping" {
			continue
		}
		if path, ok := p.javaAnnotationValue(ann, "value"); ok && path != "" {
			return path
		}
	}
	return ""
}

// javaHTTPRoute emits a producer for a Spring-mapped method, prefixing base.
func (p *fileCtx) javaHTTPRoute(m *sitter.Node, base string) {
	for _, ann := range javaAnnotations(m) {
		name := p.javaAnnotationName(ann)
		method := ""
		if hm, ok := javaMappingMethods[name]; ok {
			method = hm
		} else if name == "RequestMapping" {
			method = "GET" // default when no method= attr
			if rm, ok := p.javaAnnotationValue(ann, "method"); ok {
				method = javaRequestMethod(rm)
			} else if rm := p.javaRequestMethodField(ann); rm != "" {
				method = rm
			}
		} else {
			continue
		}
		path, _ := p.javaAnnotationValue(ann, "value") // "" is valid (base-only route)
		full := joinRoutePath(base, path)             // Spring joins method path onto class base
		if !strings.HasPrefix(full, "/") {
			continue
		}
		key := contracts.NormalizeHTTPKey(method, full)
		p.addContract(graph.KindHTTPEndpoint, key, map[string]string{"method": method, "path": full})
	}
}

// javaRequestMethod maps a RequestMethod token (with or without an enum prefix)
// to its canonical HTTP method: "RequestMethod.PUT"/"PUT" → "PUT".
func javaRequestMethod(raw string) string {
	if i := strings.LastIndexByte(raw, '.'); i >= 0 {
		raw = raw[i+1:]
	}
	switch strings.ToUpper(raw) {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return strings.ToUpper(raw)
	}
	return "GET"
}

// javaRequestMethodField reads a method=RequestMethod.X element_value_pair whose
// value is a field_access (not a string), returning "X" or "".
func (p *fileCtx) javaRequestMethodField(ann *sitter.Node) string {
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
		return ""
	}
	for _, pair := range namedChildrenOfType(al, "element_value_pair") {
		if pair.NamedChildCount() < 2 || pair.NamedChild(0).Content(p.src) != "method" {
			continue
		}
		return javaRequestMethod(pair.NamedChild(1).Content(p.src))
	}
	return ""
}

// joinRoutePath joins a class-level base path with a method-level path, inserting
// a single separating slash when needed. Doubled slashes are harmless — the HTTP
// normalizer collapses them — but a MISSING slash would corrupt the key.
func joinRoutePath(base, path string) string {
	switch {
	case base == "":
		return path
	case path == "":
		return base
	case strings.HasSuffix(base, "/"), strings.HasPrefix(path, "/"):
		return base + path
	default:
		return base + "/" + path
	}
}

// javaRestTemplateVerbs maps a Spring RestTemplate method to its HTTP method.
// The URL literal is the first argument for all of these; the httpURLPath gate at
// emit time rejects any non-URL first arg, so generic-named calls stay out.
var javaRestTemplateVerbs = map[string]string{
	"getForObject": "GET", "getForEntity": "GET",
	"postForObject": "POST", "postForEntity": "POST", "postForLocation": "POST",
	"put": "PUT", "delete": "DELETE", "patchForObject": "PATCH",
}

// javaHTTPClient emits a consumer for a RestTemplate call with a string-literal URL.
func (p *fileCtx) javaHTTPClient(call *sitter.Node) {
	_, verb := javaCallTarget(p, call)
	args := call.ChildByFieldName("arguments")
	url, ok := p.javaFirstArgString(args)
	if !ok {
		return
	}
	if verb == "exchange" {
		method := javaHTTPMethodArg(p, args)
		if method == "" {
			return // couldn't read HttpMethod.X — don't guess
		}
		p.emitHTTPClient(method, url)
		return
	}
	if m, ok := javaRestTemplateVerbs[verb]; ok {
		p.emitHTTPClient(m, url)
	}
}

// javaHTTPMethodArg scans an argument_list for an HttpMethod.X field_access and
// returns the canonical method X, or "".
func javaHTTPMethodArg(p *fileCtx, args *sitter.Node) string {
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c.Type() != "field_access" {
			continue
		}
		txt := c.Content(p.src)
		if !strings.HasPrefix(txt, "HttpMethod.") {
			continue
		}
		return javaRequestMethod(txt)
	}
	return ""
}
