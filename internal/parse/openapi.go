package parse

// OpenAPI/Swagger specs are structured data (YAML or JSON), not code, so they do
// NOT go through the tree-sitter Language path. This file adds a parallel spec
// handler: detect a spec file, unmarshal its paths, and emit each operation as a
// KindHTTPEndpoint keyed by the SAME contracts.NormalizeHTTPKey the code-level
// HTTP extractor uses. That single shared key is the whole trick — an OpenAPI
// producer and a code-discovered http_client consumer collide on it, so an
// endpoint a service documents but whose route the code extractor missed still
// pairs at resolve time with no change to resolve.go (an OpenAPI endpoint is just
// another KindHTTPEndpoint).
//
// Precision over recall, same as everywhere else: a false endpoint costs more
// trust than a missed one, so detection is conservative (a spec must clearly look
// like an OpenAPI doc) and only the eight OpenAPI operation verbs are emitted.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cpuchip/lodestar/internal/contracts"
	"github.com/cpuchip/lodestar/internal/graph"
)

// reOpenAPIMarker sniffs a top-level `openapi: 3.x` (OpenAPI 3) or `swagger: 2.x`
// (Swagger 2) version key, in either YAML (`openapi: 3.0.3`) or JSON
// (`  "openapi": "3.0.0"`) form. Requiring the value to start with 2 or 3 keeps a
// stray key named `openapi`/`swagger` in an unrelated config from matching — the
// same shape found ZERO false positives across the corpus's many k8s / workflow /
// helm YAMLs.
var reOpenAPIMarker = regexp.MustCompile(`(?m)^[ \t]*["']?(openapi|swagger)["']?[ \t]*:[ \t]*["']?[23]`)

// openAPIMethods whitelists the OpenAPI Operation-Object verbs (lower-cased key →
// canonical HTTP method). It is the full spec set — the seven common verbs plus
// trace — because a `trace:` under a path item IS an endpoint by the spec, and the
// whitelist form means the non-operation path-item keys (parameters, servers,
// summary, $ref, x-*) are ignored for free.
var openAPIMethods = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "delete": "DELETE",
	"patch": "PATCH", "head": "HEAD", "options": "OPTIONS", "trace": "TRACE",
}

// openAPIDoc is the minimal shape we need: the version marker and the paths map.
// Paths values are `any` (not a stricter map type) so a path item that is a bare
// $ref string, or an operation whose value is not a mapping, does not fail the
// whole unmarshal — we type-assert per path and skip anything that is not a
// mapping (precision over recall).
type openAPIDoc struct {
	OpenAPI string         `yaml:"openapi"`
	Swagger string         `yaml:"swagger"`
	Paths   map[string]any `yaml:"paths"`
}

// isOpenAPISpec reports whether a file is an OpenAPI/Swagger document worth
// parsing. It is deliberately conservative: the extension must be .yaml/.yml/.json,
// and then EITHER the filename is canonical (starts with "openapi"/"swagger") OR
// the content carries a top-level version marker. A non-canonically-named spec is
// content-sniffed once here; every other YAML/JSON in the tree is rejected cheaply.
func isOpenAPISpec(path, name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml", ".json":
	default:
		return false
	}
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "openapi") || strings.HasPrefix(lower, "swagger") {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return reOpenAPIMarker.Match(data)
}

// parseOpenAPI reads an OpenAPI/Swagger spec and emits one KindHTTPEndpoint per
// documented operation, keyed by contracts.NormalizeHTTPKey so it pairs with code
// clients. It mirrors parseFile's node shape — a KindFile node the endpoints hang
// off via contains edges — so a spec-derived endpoint is indistinguishable from a
// code-derived one to the resolver, except for its "source":"openapi" metadata. A
// malformed spec that slipped past detection yields no endpoints rather than
// aborting the walk (an I/O error still propagates).
func parseOpenAPI(g *graph.Graph, world, rel, absPath string) error {
	src, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil // sniffed as a spec but does not parse cleanly — skip, don't fail the walk
	}

	fileID := world + "::" + rel
	g.Nodes = append(g.Nodes, graph.Node{ID: fileID, World: world, Kind: graph.KindFile, Name: rel})
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g, seen: map[string]bool{fileID: true}}

	// Collect then sort before emitting: map iteration order is randomized, but the
	// package contract is "same input → same graph", so node order must be stable.
	type endpoint struct{ method, path, key string }
	var eps []endpoint
	for rawPath, item := range doc.Paths {
		if !strings.HasPrefix(rawPath, "/") {
			continue // OpenAPI paths start with "/"; skips paths-level x- extensions
		}
		ops, ok := item.(map[string]any) // yaml.v3 decodes mappings to map[string]any
		if !ok {
			continue // a $ref string or non-mapping path item — nothing to emit
		}
		for verb := range ops {
			method, ok := openAPIMethods[strings.ToLower(verb)]
			if !ok {
				continue // not an operation verb (parameters, summary, servers, $ref, x-*)
			}
			eps = append(eps, endpoint{method, rawPath, contracts.NormalizeHTTPKey(method, rawPath)})
		}
	}
	sort.Slice(eps, func(i, j int) bool {
		if eps[i].path != eps[j].path {
			return eps[i].path < eps[j].path
		}
		return eps[i].method < eps[j].method
	})
	for _, e := range eps {
		p.addContract(graph.KindHTTPEndpoint, e.key, map[string]string{"method": e.method, "path": e.path, "source": "openapi"})
	}
	return nil
}

// TODO(graphql): GraphQL is the sibling spec protocol to OpenAPI and was assessed
// alongside this work, then DEFERRED. smacker/go-tree-sitter bundles no graphql
// grammar, and a clean pass (schema `type Query`/`Mutation` fields as producers,
// client `gql`query{...}`` operations as consumers, keyed by operation/field name)
// needs a NEW producer/consumer Kind pair in internal/graph plus a pairing entry in
// internal/resolve — both out of bounds for this change. It is not hacked in here:
// a lightweight GraphQL extractor that had to key onto KindHTTPEndpoint would emit
// false HTTP cross-edges. Left for a follow-up that owns the graph/resolve edits.
