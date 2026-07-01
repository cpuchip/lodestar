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
