// Package contracts holds the cross-service resolver layer: per-protocol
// extractors that emit a producer side and a consumer side, each carrying a
// normalized canonical key, plus the deterministic key-normalizers that pair
// them. The normalizers are the floor everything stands on — they are pure,
// deterministic functions, so they ARE the oracle.
//
// This file ports the HTTP normalizer (the first one), kept byte-for-byte
// equivalent to pg-ai-stewards' SQL stewards.normalize_http_key so the extractor
// and the store agree on what "the same endpoint" means.
package contracts

import (
	"regexp"
	"strings"
)

var (
	reHTTPParam  = regexp.MustCompile(`\{[^/}]+\}|\$\{[^/}]+\}|:[A-Za-z_][A-Za-z0-9_]*`)
	reHTTPNum    = regexp.MustCompile(`/[0-9]+`)
	reHTTPPrefix = regexp.MustCompile(`^/(api|v[0-9]+)(/|$)`)
	reHTTPSlash  = regexp.MustCompile(`//+`)
)

// NormalizeHTTPKey produces the canonical contract key for an HTTP route or
// client call, so a producer and a consumer that mean the same endpoint collide:
//
//	GET /api/users/123  ==  GET /users/{id}  →  "GET /users/{}"
//
// Method is upper-cased; the query string is dropped; path params ({id}, :id,
// ${id}) and numeric segments collapse to {}; one leading /api or /vN is
// stripped; doubled slashes collapse. Deterministic by design — cross-service
// linking is a key-join, not fuzzy discovery, so this function is the oracle.
func NormalizeHTTPKey(method, path string) string {
	m := strings.ToUpper(method)
	if m == "" {
		m = "GET"
	}
	if path == "" {
		path = "/"
	}
	if i := strings.IndexByte(path, '?'); i >= 0 { // drop query string
		path = path[:i]
	}
	if path == "" {
		path = "/"
	}
	path = reHTTPParam.ReplaceAllString(path, "{}") // {id} / ${id} / :id -> {}
	path = reHTTPNum.ReplaceAllString(path, "/{}")  // /123 -> /{}
	path = reHTTPPrefix.ReplaceAllString(path, "/") // strip one leading /api or /vN
	path = reHTTPSlash.ReplaceAllString(path, "/")  // // -> /
	return m + " " + path
}
