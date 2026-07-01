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
	var catalog graph.Node
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindGRPCService:
			producers[n.Name] = true
			if n.Name == "ProductCatalogService" && n.Metadata["methods"] != "" {
				catalog = n
			}
		case graph.KindGRPCClient:
			consumers[n.Name] = true
		}
	}

	// recall — proto services + the server registration are producers
	for _, w := range []string{"ProductCatalogService", "ShippingService"} {
		if !producers[w] {
			t.Errorf("recall: missing gRPC producer %q (got %v)", w, keys(producers))
		}
	}
	// recall — the client ctor is a consumer
	if !consumers["ShippingService"] {
		t.Errorf("recall: missing gRPC consumer ShippingService (got %v)", keys(consumers))
	}

	// precision — generic/non-gRPC client ctors are NOT services
	if consumers["Redis"] || consumers[""] || consumers["Client"] {
		t.Errorf("precision: non-gRPC client ctor leaked: %v", keys(consumers))
	}

	// proto metadata survived (methods + package), for later method-level use
	if catalog.Metadata["package"] != "oteldemo" || catalog.Metadata["methods"] != "GetProduct ListProducts" {
		t.Errorf("proto metadata = %v, want package=oteldemo methods='GetProduct ListProducts'", catalog.Metadata)
	}
}
