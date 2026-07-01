package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
	"github.com/cpuchip/lodestar/internal/resolve"
)

// --- unit oracle: the pure raw-template → path transform ---

// TestTemplatePathFromRaw is the deterministic oracle for the reconstruction: given
// a raw template (literal text interleaved with urlExprVar sentinels for dynamic
// parts), it must yield the path a route template normalizes to — or skip when there
// is no literal path. No tree-sitter here; this is the transform in isolation.
func TestTemplatePathFromRaw(t *testing.T) {
	V := urlExprVar
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"base + \"/path/\" + id", V + "/api/v1/svc/things/" + V, "/api/v1/svc/things/{}", true},
		{"template ${base}/users/${uid}", V + "/users/" + V, "/users/{}", true},
		{"host + \"/orders\"", V + "/orders", "/orders", true},
		{"two literals with scheme", "http://h/x", "/x", true}, // "http://h" + "/x"
		{"two path params", V + "/api/v1/priceservice/prices/" + V + "/" + V, "/api/v1/priceservice/prices/{}/{}", true},
		{"adjacent vars collapse", "/x/" + V + V, "/x/{}", true},
		{"glued trailing var dropped", "/stationservice" + V, "/stationservice", true},
		// a var glued to a query key (q=) is dropped like any glued trailing var; the
		// path before "?" is what survives, and NormalizeHTTPKey drops the query anyway.
		{"query value var dropped", "/search?q=" + V, "/search?q=", true},

		// precision: fully dynamic / no literal segment → skip (no edge)
		{"fully dynamic base + path", V + V, "", false},
		{"only slash + var", V + "/" + V, "", false},
		{"scheme + host, no path", "http://host", "", false},
		{"no slash anywhere", V + "host" + V, "", false},
	}
	for _, c := range cases {
		got, ok := templatePathFromRaw(c.raw)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: templatePathFromRaw(%q) = (%q,%v), want (%q,%v)", c.name, c.raw, got, ok, c.want, c.ok)
		}
	}
}

// httpConsumers parses a single source file into world "svc" and returns the set of
// http_client contract keys it emitted.
func httpConsumers(t *testing.T, filename, src string) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	out := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindHTTPClient {
			out[n.Name] = true
		}
	}
	return out
}

// assertConsumers checks the emitted consumer set is exactly want (recall +
// precision): every wanted key present, and no extras (a fully-dynamic URL in each
// sample must be skipped).
func assertConsumers(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	for _, w := range want {
		if !got[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(got))
		}
	}
	if len(got) != len(want) {
		t.Errorf("precision: got %d consumers, want %d: %v", len(got), len(want), keys(got))
	}
}

// --- per-language: a concatenated / interpolated client URL becomes a consumer ---

func TestReconstructGoHTTP(t *testing.T) {
	const src = `package svc
import "net/http"
func call(host, base, id, oid string) {
	http.Get(host + "/products/" + id)                       // GET /products/{}
	req, _ := http.NewRequest("POST", base+"/orders/"+oid, nil) // POST /orders/{}
	http.Get(host + "/orders")                               // GET /orders
	http.Get(someVar)                                        // fully dynamic — skipped
	_ = req
}
`
	assertConsumers(t, httpConsumers(t, "c.go", src),
		"GET /products/{}", "POST /orders/{}", "GET /orders")
}

func TestReconstructPythonHTTP(t *testing.T) {
	const src = `
def call(base, uid, oid):
    requests.get(base + "/users/" + uid)   # GET /users/{}
    httpx.get(f"{base}/orders/{oid}")      # GET /orders/{}  (f-string)
    requests.get(base + "/health")         # GET /health
    requests.get(fully + dynamic)          # no literal path — skipped
`
	assertConsumers(t, httpConsumers(t, "c.py", src),
		"GET /users/{}", "GET /orders/{}", "GET /health")
}

func TestReconstructTSHTTP(t *testing.T) {
	// backticks in the template literal → build with an explicit rune.
	bq := "`"
	src := `function call(base, id, oid) {
  axios.get(base + "/users/" + id);          // GET /users/{}
  fetch(` + bq + `${base}/orders/${oid}` + bq + `);   // GET /orders/{}  (template)
  http.get(base + "/health");                // GET /health
  fetch(fullyDynamic);                       // no literal path — skipped
}
`
	assertConsumers(t, httpConsumers(t, "c.ts", src),
		"GET /users/{}", "GET /orders/{}", "GET /health")
}

func TestReconstructCSharpHTTP(t *testing.T) {
	const src = `using System.Net.Http;
namespace shop {
  public class Client {
    public void Call(string baseUrl, string id) {
      httpClient.GetAsync(baseUrl + "/api/v1/svc/things/" + id);  // GET /v1/svc/things/{}
      httpClient.GetAsync($"{baseUrl}/orders/{id}");             // GET /orders/{} (interp)
      httpClient.GetAsync(fullyDynamic);                         // no literal path — skipped
    }
  }
}
`
	assertConsumers(t, httpConsumers(t, "C.cs", src),
		"GET /v1/svc/things/{}", "GET /orders/{}")
}

// TestReconstructJavaHTTP covers the train-ticket idiom directly: a concatenated
// exchange() URL and a concatenated getForObject() URL both become consumers, AND
// the @GetMapping(path=...) producer (path is a Spring alias for value — the form
// train-ticket uses most, and one the value-only reader dropped) lands on the SAME
// key as the exchange consumer.
func TestReconstructJavaHTTP(t *testing.T) {
	const src = `package shop;
import org.springframework.web.bind.annotation.*;
import org.springframework.http.HttpMethod;

@RestController
@RequestMapping("/api/v1/stationservice")
class StationController {
  @GetMapping(path = "/stations/id/{stationName}")
  public String byId(String n) { return null; }
}

class BasicServiceImpl {
  public void callOut(String station_service_url, String train_url, String stationName) {
    restTemplate.exchange(
        station_service_url + "/api/v1/stationservice/stations/id/" + stationName,
        HttpMethod.GET, requestEntity, String.class);
    restTemplate.getForObject(train_url + "/api/v1/trainservice/trains/byNames", String.class);
  }
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Svc.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}
	producers, consumers := map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindHTTPEndpoint:
			producers[n.Name] = true
		case graph.KindHTTPClient:
			consumers[n.Name] = true
		}
	}

	// producer via the path= alias (would be missing before the fix)
	if !producers["GET /v1/stationservice/stations/id/{}"] {
		t.Errorf("producer: @GetMapping(path=...) missing (got %v)", keys(producers))
	}
	// concatenated consumers
	assertConsumers(t, consumers,
		"GET /v1/stationservice/stations/id/{}", "GET /v1/trainservice/trains/byNames")
	// the exchange consumer shares its key with the producer — the whole point
	if !(producers["GET /v1/stationservice/stations/id/{}"] && consumers["GET /v1/stationservice/stations/id/{}"]) {
		t.Error("pairing: producer and concatenated consumer must share GET /v1/stationservice/stations/id/{}")
	}
}

// --- cross-world resolve: a producer in one world pairs with a concatenated /
// templated client call in another on the same normalized key ---

// mergeGraphs combines per-world graphs into one so resolve.Resolve can join them.
func mergeGraphs(worlds ...*graph.Graph) *graph.Graph {
	m := &graph.Graph{}
	for _, g := range worlds {
		m.Worlds = append(m.Worlds, g.Worlds...)
		m.Nodes = append(m.Nodes, g.Nodes...)
		m.Edges = append(m.Edges, g.Edges...)
	}
	return m
}

func parseWorld(t *testing.T, world, filename, src string) *graph.Graph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir(world, dir)
	if err != nil {
		t.Fatalf("ParseDir(%s): %v", world, err)
	}
	return g
}

func hasHTTPCrossEdge(g *graph.Graph, key string) bool {
	for _, e := range g.CrossEdges {
		if e.Protocol == "http" && e.ContractKey == key {
			return true
		}
	}
	return false
}

// TestCrossWorldConcatResolveJava: a Spring @GetMapping(path=...) producer in
// station-service pairs with a concatenated restTemplate.exchange() call in
// basic-service — the train-ticket seam, end to end through resolve.
func TestCrossWorldConcatResolveJava(t *testing.T) {
	producer := parseWorld(t, "station-service", "StationController.java", `package shop;
import org.springframework.web.bind.annotation.*;
@RestController
@RequestMapping("/api/v1/stationservice")
class StationController {
  @GetMapping(path = "/stations/id/{stationName}")
  public String byId(String n) { return null; }
}
`)
	consumer := parseWorld(t, "basic-service", "BasicServiceImpl.java", `package shop;
import org.springframework.http.HttpMethod;
class BasicServiceImpl {
  public void callOut(String station_service_url, String stationName) {
    restTemplate.exchange(
        station_service_url + "/api/v1/stationservice/stations/id/" + stationName,
        HttpMethod.GET, requestEntity, String.class);
  }
}
`)
	merged := mergeGraphs(producer, consumer)
	resolve.Resolve(merged)
	if !hasHTTPCrossEdge(merged, "GET /v1/stationservice/stations/id/{}") {
		t.Errorf("cross-world: no http edge on GET /v1/stationservice/stations/id/{} (cross edges: %v)", crossKeys(merged))
	}
}

// TestCrossWorldConcatResolveTS: an Express app.get(":id") producer pairs with a
// backtick-template fetch() consumer in another world.
func TestCrossWorldConcatResolveTS(t *testing.T) {
	bq := "`"
	producer := parseWorld(t, "route-service", "routes.ts", `function routes(app) {
  app.get("/api/v1/routeservice/routes/:routeId", getRoute);
}
`)
	consumer := parseWorld(t, "basic-service", "client.ts", `function call(base, routeId) {
  fetch(`+bq+`${base}/api/v1/routeservice/routes/${routeId}`+bq+`);
}
`)
	merged := mergeGraphs(producer, consumer)
	resolve.Resolve(merged)
	if !hasHTTPCrossEdge(merged, "GET /v1/routeservice/routes/{}") {
		t.Errorf("cross-world: no http edge on GET /v1/routeservice/routes/{} (cross edges: %v)", crossKeys(merged))
	}
}

func crossKeys(g *graph.Graph) []string {
	var out []string
	for _, e := range g.CrossEdges {
		out = append(out, e.Protocol+" "+e.ContractKey)
	}
	return out
}
