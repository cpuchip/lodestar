package parse

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// This file adds the ONE thing every HTTP consumer extractor was missing: the
// ability to key a client call whose URL is BUILT by concatenation or string
// interpolation, not written as a single literal. Service-discovery code almost
// never hard-codes a full URL — it does
//
//	restTemplate.exchange(baseUrl + "/api/v1/stationservice/stations/" + id, ...)   // Java
//	http.Get(host + "/products/" + id)                                              // Go
//	requests.get(f"{base}/users/{uid}")                                             // Python
//	fetch(`${base}/users/${id}`)                                                    // TS/JS
//
// The literal-only extractors skip all of these, so a discovery-based monolith like
// train-ticket surfaces almost no HTTP cross-edges. reconstructHTTPPath rebuilds a
// templated PATH from such an expression (every dynamic sub-part collapsed to a
// single {}), which NormalizeHTTPKey then reduces to the same key a route template
// (@GetMapping("/api/v1/.../stations/{id}")) already produces — so the two pair.
//
// Precision over recall throughout: a URL with no literal "/"-led path segment is
// skipped (no edge), because a false cross-edge costs more trust than a missed one.

// urlExprVar is the sentinel that stands in for a dynamic sub-expression (a
// variable, an interpolation, an unrecognized node) while a URL expression is being
// flattened. It uses NUL bytes so it can never collide with real source text.
const urlExprVar = "\x00VAR\x00"

var (
	// reURLAdjacentVars collapses a run of adjacent placeholders ("{}{}", from
	// `a + b`) down to one, so two consecutive dynamic parts read as a single
	// variable segment rather than "{}{}".
	reURLAdjacentVars = regexp.MustCompile(`(?:\{\})+`)
	// reURLSlash collapses doubled slashes (NormalizeHTTPKey does this too; doing it
	// here keeps the reconstructed path clean before the literal-segment gate).
	reURLSlash = regexp.MustCompile(`//+`)
)

// reconstructHTTPPath rebuilds a normalized-ready path from a URL-argument node that
// may be a string concatenation or an interpolated/template string. It returns the
// path (each dynamic segment collapsed to {}) and true, or ("",false) when the whole
// URL is dynamic — i.e. no literal path segment beginning with "/" is present.
//
// It is the fallback the consumer extractors reach for AFTER the plain-literal path
// fails: literal first (keeps existing behavior byte-for-byte), reconstruction only
// when there is no single literal to read.
func (p *fileCtx) reconstructHTTPPath(n *sitter.Node) (string, bool) {
	raw, hasLiteral := p.urlExprRaw(n)
	if !hasLiteral {
		return "", false // fully dynamic — no literal text anywhere
	}
	return templatePathFromRaw(raw)
}

// urlExprRaw flattens a URL-argument expression into a raw template: every
// string-literal fragment is kept verbatim, every dynamic sub-expression becomes a
// urlExprVar sentinel. hasLiteral reports whether ANY literal text was seen (the
// precision gate — a fully-dynamic URL must be skipped). One switch covers the node
// types of all five languages; the type names are distinct enough to be unambiguous
// (verified by probing each grammar):
//
//	Go     concat: binary_expression (+)   · literal: interpreted/raw_string_literal
//	Java   concat: binary_expression (+)   · literal: string_literal
//	C#     concat: binary_expression (+)   · literal: string_literal · interp: interpolated_string_expression
//	Python concat: binary_operator (+)     · literal/f-string: string
//	TS/JS  concat: binary_expression (+)   · literal: string · template: template_string
func (p *fileCtx) urlExprRaw(n *sitter.Node) (raw string, hasLiteral bool) {
	if n == nil {
		return "", false
	}
	switch n.Type() {
	case "interpreted_string_literal", "raw_string_literal": // Go string literals
		if s, ok := p.stringLit(n); ok {
			return s, true
		}
		return urlExprVar, false
	case "string", "string_literal", "template_string", "interpolated_string_expression":
		// Python/TS `string`, Java/C# `string_literal`, TS `template_string`, C#
		// `interpolated_string_expression` — each interleaves literal fragments with
		// interpolations; urlStringParts flattens them uniformly.
		return p.urlStringParts(n)
	case "binary_expression", "binary_operator": // "+" concatenation (all langs)
		if op := n.ChildByFieldName("operator"); op == nil || op.Content(p.src) != "+" {
			return urlExprVar, false // a non-"+" operator is not string building
		}
		l, lLit := p.urlExprRaw(n.ChildByFieldName("left"))
		r, rLit := p.urlExprRaw(n.ChildByFieldName("right"))
		return l + r, lLit || rLit
	case "argument": // C# wraps each call argument in an `argument` node
		if n.NamedChildCount() > 0 {
			return p.urlExprRaw(n.NamedChild(0))
		}
		return urlExprVar, false
	default:
		return urlExprVar, false // identifier / call / member access / etc. → dynamic
	}
}

// urlStringParts flattens a string / template / interpolated-string node into raw
// template text: literal fragments are appended verbatim; interpolations become a
// urlExprVar sentinel. Covers Python f-strings, TS/JS template strings, C#
// interpolated strings, and plain string literals of every grammar in one loop. The
// grammars name the pieces differently (string_content / string_fragment /
// string_literal_content for literal text; interpolation / template_substitution for
// dynamic parts), so all the known aliases are matched.
func (p *fileCtx) urlStringParts(n *sitter.Node) (string, bool) {
	var sb strings.Builder
	hasLiteral := false
	for i := 0; i < int(n.NamedChildCount()); i++ {
		switch c := n.NamedChild(i); c.Type() {
		case "string_content", "string_fragment", "string_literal_content", "interpolated_string_text":
			sb.WriteString(c.Content(p.src))
			hasLiteral = true
		case "interpolation", "template_substitution":
			sb.WriteString(urlExprVar)
		}
		// string_start / string_end / interpolation_start / escape_sequence: ignored.
	}
	return sb.String(), hasLiteral
}

// templatePathFromRaw turns a raw URL template (literal text interleaved with
// urlExprVar sentinels) into a path whose variable segments are {}:
//
//	"\x00VAR\x00/api/v1/svc/things/\x00VAR\x00"  →  "/api/v1/svc/things/{}"
//	"http://h/x"                                 →  "/x"
//	"\x00VAR\x00/\x00VAR\x00"                     →  ("", false)   // no literal segment
//
// It drops the scheme+host (keeps from the first "/"), replaces each sentinel with
// {}, collapses adjacent placeholders and doubled slashes, drops a trailing var glued
// to a non-slash char, and requires at least one real literal path segment. Pure
// string transform — the unit oracle for the reconstruction.
func templatePathFromRaw(raw string) (string, bool) {
	path := extractRawPath(raw)
	if path == "" {
		return "", false // no "/"-led path — the whole URL is a bare host/var
	}
	path = strings.ReplaceAll(path, urlExprVar, "{}")
	path = reURLAdjacentVars.ReplaceAllString(path, "{}") // "{}{}" (adjacent vars) → "{}"
	path = reURLSlash.ReplaceAllString(path, "/")         // "//"  → "/"
	// Drop a trailing var glued to a non-slash char ("…/stationservice{}" →
	// "…/stationservice"); a clean "…/users/{}" (var is its own segment) is kept,
	// since that is exactly what pairs with a "/users/{id}" route template.
	if strings.HasSuffix(path, "{}") {
		if base := path[:len(path)-2]; base != "" && !strings.HasSuffix(base, "/") {
			path = base
		}
	}
	if !strings.HasPrefix(path, "/") || !hasLiteralSegment(path) {
		return "", false // guard: only emit when a literal path segment is present
	}
	return path, true
}

// extractRawPath returns the path portion of a raw URL template: for
// "scheme://host/rest" it keeps "/rest"; otherwise it keeps from the first "/"
// (dropping any leading host variable). It returns "" when there is no "/"-led path
// at all (a bare host, or "scheme://host" with no path). Manual scan (not url.Parse)
// because the raw string can contain NUL sentinel bytes.
func extractRawPath(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if s := strings.IndexByte(rest, '/'); s >= 0 {
			return rest[s:]
		}
		return "" // scheme + host, no path
	}
	if s := strings.IndexByte(raw, '/'); s >= 0 {
		return raw[s:]
	}
	return ""
}

// hasLiteralSegment reports whether a path has at least one "/"-delimited segment
// that is a real literal (non-empty and not a "{}" placeholder). A path that is only
// slashes and variables ("/", "/{}", "/{}/{}") carries no route identity and is
// skipped — its real path lives inside the base variable we could not see.
func hasLiteralSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg != "" && seg != "{}" {
			return true
		}
	}
	return false
}

// emitHTTPClientPath emits a consumer node for a client call whose PATH is already
// extracted (from a full-URL literal via httpURLPath, or a reconstructed template).
// raw is kept as metadata for navigation. The shared tail of every consumer emit.
func (p *fileCtx) emitHTTPClientPath(method, path, raw string) {
	if path == "" {
		return
	}
	key := contracts.NormalizeHTTPKey(method, path)
	p.addContract(graph.KindHTTPClient, key, map[string]string{"method": method, "path": path, "url": raw})
}

// emitHTTPClientFromNode emits a consumer for a URL-argument node, trying the plain
// literal first (isLit set, via the language's own string extractor — keeps existing
// behavior exactly) and falling back to reconstructing a templated path from a
// concatenation/interpolation. Skips silently when both fail (precision over recall).
func (p *fileCtx) emitHTTPClientFromNode(method string, node *sitter.Node, lit string, isLit bool) {
	if isLit {
		p.emitHTTPClient(method, lit) // full-URL literal → httpURLPath, unchanged path
		return
	}
	if node == nil {
		return
	}
	if path, ok := p.reconstructHTTPPath(node); ok {
		p.emitHTTPClientPath(method, path, node.Content(p.src))
	}
}
