package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// The HTTP extractor's oracle: recall (routes across net/http, gin/echo, chi and
// client calls all surface with the right normalized key), precision (dynamic
// paths are skipped; http.Get is a consumer not a route), disambiguation (a
// producer and a consumer that mean the same endpoint share a key — the whole
// point), and method-in-pattern (Go 1.22 "POST /orders").
const sampleHTTPGo = `package svc

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

func routes(mux *http.ServeMux, r *gin.Engine) {
	mux.HandleFunc("/users/{id}", handleUser)
	mux.HandleFunc("POST /orders", handleOrder)
	r.GET("/products/:id", getProduct)
	r.POST("/products", createProduct)
	http.HandleFunc("/health", handleHealth)

	dynamic := "/x/" + someID
	mux.HandleFunc(dynamic, h)     // non-literal path — must be skipped
	cache.Get("some-cache-key")    // not a route — must be skipped
}

func callDownstream() {
	http.Get("http://catalog/products/42")
	req, _ := http.NewRequest("POST", "http://catalog/products", nil)
	http.NewRequestWithContext(context.Background(), "GET", "http://users/users/7", nil)
	_ = req
}
`

func parseHTTPSample(t *testing.T) *graph.Graph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http.go"), []byte(sampleHTTPGo), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	return g
}

func TestExtractGoHTTP(t *testing.T) {
	g := parseHTTPSample(t)

	producers := map[string]bool{}
	consumers := map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindHTTPEndpoint:
			producers[n.Name] = true
		case graph.KindHTTPClient:
			consumers[n.Name] = true
		}
	}

	// recall — producers, keyed and normalized
	wantProducers := []string{
		"GET /users/{}",    // net/http HandleFunc, {id}->{}
		"POST /orders",     // Go 1.22 method-in-pattern
		"GET /products/{}", // gin r.GET, :id->{}
		"POST /products",   // gin r.POST
		"GET /health",      // extracted here; noise-filtered later at resolve
	}
	for _, w := range wantProducers {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}

	// recall — consumers
	wantConsumers := []string{
		"GET /products/{}", // http.Get("http://catalog/products/42")
		"POST /products",   // http.NewRequest("POST", ".../products")
		"GET /users/{}",    // http.NewRequestWithContext(... "GET", ".../users/7")
	}
	for _, w := range wantConsumers {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}

	// disambiguation — a producer and a consumer collide on the SAME key (this is
	// the whole point: gin's GET /products/{} route pairs with the http.Get call)
	if !(producers["GET /products/{}"] && consumers["GET /products/{}"]) {
		t.Error("disambiguation: GET /products/{} should be BOTH a producer and a consumer")
	}

	// precision — dynamic path + cache.Get were skipped: no bogus producers
	if producers["GET /x/{}"] || producers["GET some-cache-key"] || len(producers) != len(wantProducers) {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}
	// precision — a different method/route does not collapse
	if consumers["GET /products"] {
		t.Error("precision: POST /products must not read as GET /products")
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
