package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

const grpcProto = `syntax = "proto3";
package oteldemo;

service ProductCatalogService {
  rpc GetProduct(GetProductRequest) returns (Product) {}
  rpc ListProducts(Empty) returns (ListProductsResponse) {}
}

service ShippingService {
  rpc GetQuote(GetQuoteRequest) returns (GetQuoteResponse) {}
}
`

const grpcGo = `package svc

import (
	"example.com/pb"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

func serve(s *grpc.Server, impl pb.ProductCatalogServiceServer) {
	pb.RegisterProductCatalogServiceServer(s, impl)
}

func clients(conn *grpc.ClientConn) {
	_ = pb.NewShippingServiceClient(conn)
	_ = redis.NewClient(&redis.Options{}) // generic New*Client — must be skipped
	_ = pb.NewClient(conn)                // NewClient — service name empty, skipped
}
`

// The gRPC oracle: proto service defs and Go RegisterXServer are producers;
// NewXClient is a consumer; both keyed by the bare service name so the two sides
// (which don't share a proto package) meet. Precision: NewClient / NewRedisClient
// do not become gRPC services.
func TestExtractGoGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.proto"), []byte(grpcProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grpc.go"), []byte(grpcGo), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	producers := map[string]bool{}
	consumers := map[string]bool{}
	schemas := map[string]bool{}
	var catalog graph.Node
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindGRPCService:
			producers[n.Name] = true
		case graph.KindGRPCClient:
			consumers[n.Name] = true
		case graph.KindSchema:
			schemas[n.Name] = true
			if n.Name == "ProductCatalogService" && n.Metadata["methods"] != "" {
				catalog = n
			}
		}
	}

	// The producer is the SERVER REGISTRATION only — RegisterProductCatalogServiceServer.
	// A .proto that merely DECLARES a service (ShippingService here) is NOT a producer:
	// copied protos would make every world a false producer (the Online Boutique blowup).
	if !producers["ProductCatalogService"] {
		t.Errorf("recall: RegisterProductCatalogServiceServer should be a producer (got %v)", keys(producers))
	}
	if producers["ShippingService"] {
		t.Errorf("precision: a bare .proto service must NOT be a producer (ShippingService leaked)")
	}
	// the client ctor is a consumer
	if !consumers["ShippingService"] {
		t.Errorf("recall: NewShippingServiceClient should be a consumer (got %v)", keys(consumers))
	}
	// the proto records both services as SCHEMA (navigation + future method-level), not producers
	for _, s := range []string{"ProductCatalogService", "ShippingService"} {
		if !schemas[s] {
			t.Errorf("recall: proto service %q should be a schema node (got %v)", s, keys(schemas))
		}
	}
	// precision — generic/non-gRPC client ctors are NOT services
	if consumers["Redis"] || consumers[""] || consumers["Client"] {
		t.Errorf("precision: non-gRPC client ctor leaked: %v", keys(consumers))
	}
	// proto metadata survived on the schema node (methods + package)
	if catalog.Metadata["package"] != "oteldemo" || catalog.Metadata["methods"] != "GetProduct ListProducts" {
		t.Errorf("proto schema metadata = %v, want package=oteldemo methods='GetProduct ListProducts'", catalog.Metadata)
	}
}
