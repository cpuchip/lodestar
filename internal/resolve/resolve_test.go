package resolve

import (
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

func ep(world, key string) graph.Node {
	return graph.Node{ID: world + "::ep::" + key, World: world, Kind: graph.KindHTTPEndpoint, Name: key}
}
func cl(world, key string) graph.Node {
	return graph.Node{ID: world + "::cl::" + key, World: world, Kind: graph.KindHTTPClient, Name: key}
}

// The resolve oracle: a producer in world A and a consumer in world B on the same
// key become exactly one cross-edge, pointing caller→callee; same-world pairs and
// health-noise keys produce none; a producer with no consumer produces none.
func TestResolveHTTP(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"catalog", "checkout", "web"},
		Nodes: []graph.Node{
			ep("catalog", "GET /products/{}"),  // producer in catalog
			cl("checkout", "GET /products/{}"), // consumer in checkout  → 1 edge
			cl("web", "GET /products/{}"),       // consumer in web       → 1 edge
			ep("orders", "POST /orders"),        // producer, no consumer  → 0 edges
			ep("catalog", "GET /health"),        // health noise ...
			cl("web", "GET /health"),            // ... consumer too       → 0 edges (filtered)
			ep("billing", "GET /internal"),      // same-world pair ...
			cl("billing", "GET /internal"),      // ... in billing         → 0 edges (internal)
		},
	}
	Resolve(g)

	// exactly two edges: catalog's product route consumed by checkout and by web
	if len(g.CrossEdges) != 2 {
		t.Fatalf("want 2 cross-edges, got %d: %+v", len(g.CrossEdges), g.CrossEdges)
	}

	for _, e := range g.CrossEdges {
		if e.ContractKey != "GET /products/{}" {
			t.Errorf("unexpected edge on key %q", e.ContractKey)
		}
		if e.Protocol != "http" || e.Rel != "http_call" {
			t.Errorf("edge protocol/rel = %q/%q, want http/http_call", e.Protocol, e.Rel)
		}
		if e.Confidence != 0.85 {
			t.Errorf("confidence = %v, want 0.85", e.Confidence)
		}
		// direction: caller (consumer) → callee (producer)
		if e.Dst != "catalog::ep::GET /products/{}" {
			t.Errorf("edge Dst = %q, want the catalog producer (caller→callee)", e.Dst)
		}
		if e.Src != "checkout::cl::GET /products/{}" && e.Src != "web::cl::GET /products/{}" {
			t.Errorf("edge Src = %q, want a consumer", e.Src)
		}
	}
}

func svc(world, name string) graph.Node {
	return graph.Node{ID: world + "::svc::" + name, World: world, Kind: graph.KindGRPCService, Name: name}
}
func gcl(world, name string) graph.Node {
	return graph.Node{ID: world + "::gcl::" + name, World: world, Kind: graph.KindGRPCClient, Name: name}
}

// TestResolveGRPC proves the gRPC pairing joins on the bare service name across
// worlds, at 0.9 confidence, caller→callee.
func TestResolveGRPC(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"catalog", "checkout"},
		Nodes: []graph.Node{
			svc("catalog", "ProductCatalogService"),  // producer
			gcl("checkout", "ProductCatalogService"), // consumer → 1 edge
			svc("catalog", "ShippingService"),        // producer, no consumer → 0
		},
	}
	Resolve(g)
	if len(g.CrossEdges) != 1 {
		t.Fatalf("want 1 gRPC cross-edge, got %d: %+v", len(g.CrossEdges), g.CrossEdges)
	}
	e := g.CrossEdges[0]
	if e.Protocol != "grpc" || e.Rel != "grpc_call" || e.Confidence != 0.9 {
		t.Errorf("edge = %+v, want grpc/grpc_call/0.9", e)
	}
	if e.Src != "checkout::gcl::ProductCatalogService" || e.Dst != "catalog::svc::ProductCatalogService" {
		t.Errorf("direction wrong: %s -> %s (want checkout consumer -> catalog producer)", e.Src, e.Dst)
	}
}

// --- symmetric couplings: config/env + shared-DB ---

func cfg(world, name, suffix string) graph.Node {
	return graph.Node{ID: world + "::cfg::" + name + suffix, World: world, Kind: graph.KindConfigKey, Name: name}
}
func tbl(world, name string) graph.Node {
	return graph.Node{ID: world + "::tbl::" + name, World: world, Kind: graph.KindDataEntity, Name: name}
}

// edgesOn returns the cross-edges whose ContractKey equals key.
func edgesOn(g *graph.Graph, key string) []graph.CrossEdge {
	var out []graph.CrossEdge
	for _, e := range g.CrossEdges {
		if e.ContractKey == key {
			out = append(out, e)
		}
	}
	return out
}

// The config oracle: two worlds reading DATABASE_URL bind once (undirected); PORT
// shared by three worlds is noise (0 edges); a var in one world binds nothing; a var
// spanning 8 worlds is generic infra and is skipped by the maxWorlds cap.
func TestResolveConfigSymmetric(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"api", "worker"},
		Nodes: []graph.Node{
			cfg("api", "DATABASE_URL", "-a"),    // ┐ two worlds read the same URL
			cfg("worker", "DATABASE_URL", "-b"), // ┘ → exactly 1 undirected edge
			cfg("api", "PORT", ""),              // ┐ PORT across three worlds ...
			cfg("worker", "PORT", ""),           // │
			cfg("web", "PORT", ""),              // ┘ ... is infra noise → 0 edges
			cfg("api", "ONLY_HERE", ""),         // single-world var → 0 edges
		},
	}
	// a var in 8 worlds → over the maxWorlds cap → 0 edges
	for _, w := range []string{"w1", "w2", "w3", "w4", "w5", "w6", "w7", "w8"} {
		g.Nodes = append(g.Nodes, cfg(w, "SHARED_SECRET", ""))
	}
	Resolve(g)

	db := edgesOn(g, "DATABASE_URL")
	if len(db) != 1 {
		t.Fatalf("DATABASE_URL: want 1 edge, got %d: %+v", len(db), db)
	}
	e := db[0]
	if e.Protocol != "config" || e.Rel != "reads_config" || e.Confidence != 0.7 {
		t.Errorf("edge = %+v, want config/reads_config/0.7", e)
	}
	// undirected, emitted once: src world < dst world (api < worker)
	if e.Src != "api::cfg::DATABASE_URL-a" || e.Dst != "worker::cfg::DATABASE_URL-b" {
		t.Errorf("direction/dedup wrong: %s -> %s", e.Src, e.Dst)
	}
	if n := len(edgesOn(g, "PORT")); n != 0 {
		t.Errorf("PORT is noise, want 0 edges, got %d", n)
	}
	if n := len(edgesOn(g, "ONLY_HERE")); n != 0 {
		t.Errorf("single-world var, want 0 edges, got %d", n)
	}
	if n := len(edgesOn(g, "SHARED_SECRET")); n != 0 {
		t.Errorf("8-world var exceeds maxWorlds cap, want 0 edges, got %d", n)
	}
}

// The shared-DB oracle: two worlds touching orders bind once at db/shares_table/0.75;
// information_schema is metadata noise (0 edges).
func TestResolveSharedDB(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"checkout", "shipping"},
		Nodes: []graph.Node{
			tbl("checkout", "orders"),
			tbl("shipping", "orders"), // → 1 edge on orders
			tbl("checkout", "information_schema"),
			tbl("shipping", "information_schema"), // metadata → 0 edges
			tbl("checkout", "cart"),               // single world → 0 edges
		},
	}
	Resolve(g)
	oe := edgesOn(g, "orders")
	if len(oe) != 1 {
		t.Fatalf("orders: want 1 edge, got %d: %+v", len(oe), oe)
	}
	if oe[0].Protocol != "db" || oe[0].Rel != "shares_table" || oe[0].Confidence != 0.75 {
		t.Errorf("edge = %+v, want db/shares_table/0.75", oe[0])
	}
	if n := len(edgesOn(g, "information_schema")); n != 0 {
		t.Errorf("information_schema is noise, want 0 edges, got %d", n)
	}
	if n := len(edgesOn(g, "cart")); n != 0 {
		t.Errorf("single-world table, want 0 edges, got %d", n)
	}
}

// TestSymmetricSingleEmissionAndRepresentative proves an undirected pairing emits
// each unordered world-pair exactly once (no a→b AND b→a), and that many files in
// one world collapse to one representative — so three worlds with duplicate readers
// yield exactly C(3,2)=3 edges, not one-per-file.
func TestSymmetricSingleEmissionAndRepresentative(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"a", "b", "c"},
		Nodes: []graph.Node{
			cfg("a", "TOKEN", "-1"), cfg("a", "TOKEN", "-2"), cfg("a", "TOKEN", "-3"), // 3 files, one world
			cfg("b", "TOKEN", "-1"),
			cfg("c", "TOKEN", "-1"), cfg("c", "TOKEN", "-2"),
		},
	}
	Resolve(g)
	edges := edgesOn(g, "TOKEN")
	if len(edges) != 3 { // C(3,2), NOT 3*1*2 pairwise-over-files
		t.Fatalf("want 3 edges (one per world-pair), got %d: %+v", len(edges), edges)
	}
	// each unordered world-pair present exactly once; representative = smallest ID
	seen := map[string]int{}
	for _, e := range edges {
		sw, dw := worldOfID(e.Src), worldOfID(e.Dst)
		if sw >= dw {
			t.Errorf("edge not ordered src<dst world: %s -> %s", e.Src, e.Dst)
		}
		seen[sw+"|"+dw]++
	}
	for _, pair := range []string{"a|b", "a|c", "b|c"} {
		if seen[pair] != 1 {
			t.Errorf("world-pair %s emitted %d times, want exactly 1", pair, seen[pair])
		}
	}
	// representative for world a is its smallest node ID (…TOKEN-1)
	for _, e := range edges {
		if worldOfID(e.Src) == "a" && e.Src != "a::cfg::TOKEN-1" {
			t.Errorf("world a representative = %q, want smallest ID a::cfg::TOKEN-1", e.Src)
		}
	}
}

// worldOfID recovers the world from a test node ID of the form "world::...".
func worldOfID(id string) string {
	if i := indexOf(id, ':'); i >= 0 {
		return id[:i]
	}
	return id
}
func indexOf(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// TestResolveNoCrossWhenSingleWorld proves the join needs two worlds: a full
// producer+consumer set inside one world yields nothing.
func TestResolveNoCrossWhenSingleWorld(t *testing.T) {
	g := &graph.Graph{
		Worlds: []string{"mono"},
		Nodes: []graph.Node{
			ep("mono", "GET /a/{}"), cl("mono", "GET /a/{}"),
			ep("mono", "POST /b"), cl("mono", "POST /b"),
		},
	}
	Resolve(g)
	if len(g.CrossEdges) != 0 {
		t.Fatalf("single-world graph must have 0 cross-edges, got %d", len(g.CrossEdges))
	}
}
