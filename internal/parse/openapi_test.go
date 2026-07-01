package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
	"github.com/cpuchip/lodestar/internal/resolve"
)

// The OpenAPI oracle: recall (every documented operation surfaces as a
// KindHTTPEndpoint with the SAME normalized key a code route would produce, so a
// spec pairs with code clients), metadata (source=openapi marks the origin),
// templating (OpenAPI {param} collapses to {} via NormalizeHTTPKey), and precision
// (a plain config YAML sitting in the same dir yields no endpoints).
const sampleOpenAPIYAML = `openapi: 3.0.3
info:
  title: Users API
  version: 1.0.0
paths:
  /users/{id}:
    parameters:
      - name: id
        in: path
    get:
      summary: Get a user
      responses:
        '200':
          description: ok
    delete:
      summary: Delete a user
      responses:
        '204':
          description: gone
  /orders:
    post:
      summary: Create an order
      responses:
        '201':
          description: created
`

// A non-OpenAPI YAML in the same directory — the precision guard. It must produce
// zero endpoints (no top-level openapi/swagger version marker).
const sampleConfigYAML = `database:
  host: localhost
  port: 5432
service:
  name: users
  openapi_docs_enabled: true
`

func endpointsBySource(g *graph.Graph, source string) map[string]bool {
	out := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindHTTPEndpoint && n.Metadata["source"] == source {
			out[n.Name] = true
		}
	}
	return out
}

func TestParseOpenAPIYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte(sampleOpenAPIYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// Precision decoy: a config file that even mentions "openapi" in a nested key.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(sampleConfigYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := ParseDir("users-svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	got := endpointsBySource(g, "openapi")
	want := []string{
		"GET /users/{}",    // {id} -> {} via NormalizeHTTPKey
		"DELETE /users/{}", // second operation on the same path item
		"POST /orders",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("recall: missing OpenAPI endpoint %q (got %v)", w, keys(got))
		}
	}

	// precision — the config's nested "openapi_docs_enabled" must NOT be detected as
	// a spec, and no operation-object noise leaks in: exactly the three endpoints.
	if len(got) != len(want) {
		t.Errorf("precision: expected %d openapi endpoints, got %d: %v", len(want), len(got), keys(got))
	}

	// every http_endpoint in this graph came from the spec (source metadata present)
	for _, n := range g.Nodes {
		if n.Kind == graph.KindHTTPEndpoint && n.Metadata["source"] != "openapi" {
			t.Errorf("metadata: endpoint %q missing source=openapi (got %q)", n.Name, n.Metadata["source"])
		}
	}
}

// A non-canonically-named .json spec proves two things at once: yaml.v3 parses JSON
// (a YAML superset), and the content-sniff path (not just the filename) detects it.
const sampleOpenAPIJSON = `{
  "openapi": "3.0.0",
  "info": { "title": "Catalog", "version": "1.0" },
  "paths": {
    "/products/{sku}": {
      "get": { "responses": { "200": { "description": "ok" } } }
    }
  }
}
`

func TestParseOpenAPIJSONBySniff(t *testing.T) {
	dir := t.TempDir()
	// Deliberately NOT named openapi/swagger — detection must come from content.
	if err := os.WriteFile(filepath.Join(dir, "catalog-api.json"), []byte(sampleOpenAPIJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	// A plain package.json decoy: same extension, not a spec.
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"catalog","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	g, err := ParseDir("catalog-svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	got := endpointsBySource(g, "openapi")
	if !got["GET /products/{}"] {
		t.Errorf("recall/json-sniff: missing GET /products/{} (got %v)", keys(got))
	}
	if len(got) != 1 {
		t.Errorf("precision: package.json must not be read as a spec; want 1 endpoint, got %d: %v", len(got), keys(got))
	}
}

// The payoff test: an OpenAPI endpoint in world A pairs with a code-discovered
// http_client in world B on the shared normalized key, proving spec producers flow
// through the untouched resolver exactly like code producers.
func TestOpenAPIEndpointResolvesToCodeClient(t *testing.T) {
	// World A: a service that ships an OpenAPI spec but whose route code we don't parse.
	dirA := t.TempDir()
	specA := `openapi: 3.0.0
info: {title: Users, version: "1.0"}
paths:
  /users/{id}:
    get:
      responses: {'200': {description: ok}}
`
	if err := os.WriteFile(filepath.Join(dirA, "openapi.yml"), []byte(specA), 0o644); err != nil {
		t.Fatal(err)
	}
	gA, err := ParseDir("users-svc", dirA)
	if err != nil {
		t.Fatalf("ParseDir A: %v", err)
	}

	// World B: a Go caller that hits GET /users/7 → normalizes to GET /users/{}.
	dirB := t.TempDir()
	clientB := `package client

import "net/http"

func fetch() { http.Get("http://users-svc/users/7") }
`
	if err := os.WriteFile(filepath.Join(dirB, "client.go"), []byte(clientB), 0o644); err != nil {
		t.Fatal(err)
	}
	gB, err := ParseDir("orders-svc", dirB)
	if err != nil {
		t.Fatalf("ParseDir B: %v", err)
	}

	// Merge the two worlds and resolve (the real cross-service join, unedited).
	combined := &graph.Graph{
		Worlds: []string{"users-svc", "orders-svc"},
		Nodes:  append(append([]graph.Node{}, gA.Nodes...), gB.Nodes...),
	}
	resolve.Resolve(combined)

	found := false
	for _, e := range combined.CrossEdges {
		if e.Protocol == "http" && e.ContractKey == "GET /users/{}" {
			found = true
			if e.Src == "" || e.Dst == "" {
				t.Errorf("cross-edge has empty endpoints: %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("resolve: OpenAPI producer (users-svc) did not pair with code client (orders-svc) on GET /users/{}; cross-edges: %+v", combined.CrossEdges)
	}
}
