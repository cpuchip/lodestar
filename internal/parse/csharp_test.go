package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// --- structural ---

const sampleCS = `using System;
using Grpc.Core;

namespace shop.services
{
    class BaseService {}
    interface IWorker {}

    public class CartService : BaseService, IWorker
    {
        public void Start() { Helper(); }
        private void Helper() {}
        public class Inner {
            public void Run() {}
        }
    }
    public struct Bar {}
    public interface IThing {}
}
`

func TestParseCSharpSkeleton(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CartService.cs"), []byte(sampleCS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	byKindName := map[string]graph.Node{}
	for _, n := range g.Nodes {
		byKindName[n.Kind+" "+n.Name] = n
	}

	// recall — types (incl. nested Inner + struct + interface) and methods
	want := []struct{ kind, name string }{
		{graph.KindFile, "CartService.cs"},
		{graph.KindClass, "BaseService"},
		{graph.KindInterface, "IWorker"},
		{graph.KindClass, "CartService"},
		{graph.KindMethod, "CartService.Start"},
		{graph.KindMethod, "CartService.Helper"},
		{graph.KindClass, "Inner"},
		{graph.KindMethod, "Inner.Run"},
		{graph.KindClass, "Bar"},
		{graph.KindInterface, "IThing"},
	}
	for _, w := range want {
		if _, ok := byKindName[w.kind+" "+w.name]; !ok {
			t.Errorf("recall: missing %s %q", w.kind, w.name)
		}
	}

	// precision — exactly these structural nodes (no contracts in this sample)
	structural := 0
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindFile, graph.KindClass, graph.KindInterface, graph.KindMethod, graph.KindFunction:
			structural++
		}
	}
	if structural != len(want) {
		var got []string
		for _, n := range g.Nodes {
			got = append(got, n.Kind+" "+n.Name)
		}
		t.Errorf("precision: got %d structural nodes, want %d: %v", structural, len(want), got)
	}

	// imports
	if file := byKindName[graph.KindFile+" CartService.cs"]; file.Metadata["imports"] != "System Grpc.Core" {
		t.Errorf("imports: file metadata = %q", file.Metadata["imports"])
	}

	// deep call graph — inherits (BaseService), implements (IWorker), calls (Helper)
	edges := map[string]bool{}
	for _, e := range g.Edges {
		if e.Rel != graph.RelContains {
			edges[e.Rel+" "+e.Src+" -> "+e.Dst] = true
		}
	}
	base := "svc::CartService.cs::"
	wantEdges := []string{
		graph.RelInherits + " " + base + "CartService -> " + base + "BaseService",
		graph.RelImplements + " " + base + "CartService -> " + base + "IWorker",
		graph.RelCalls + " " + base + "CartService.Start -> " + base + "CartService.Helper",
	}
	for _, w := range wantEdges {
		if !edges[w] {
			var got []string
			for e := range edges {
				got = append(got, e)
			}
			t.Errorf("call graph: missing edge %q (got %v)", w, got)
		}
	}
}

// --- http ---

const httpCS = `using Microsoft.AspNetCore.Mvc;
namespace shop
{
    [ApiController]
    [Route("/api/users")]
    public class UsersController : ControllerBase
    {
        [HttpGet("{id}")]
        public User Get(int id) { return null; }

        [HttpPost]
        public void Create() {}

        [HttpDelete("/absolute/{id}")]
        public void Remove(int id) {}

        public void CallOut() {
            httpClient.GetAsync("http://catalog/products/42");
            httpClient.PostAsync("http://catalog/orders", content);
            cache.GetAsync("not-a-url");
        }
    }
    public class Program {
        public static void Main() {
            app.MapGet("/health", handler);
            app.MapPost("/webhook", handler);
        }
    }
}
`

func TestExtractCSharpHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "UsersController.cs"), []byte(httpCS), 0o644); err != nil {
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

	// producers — attribute routing (relative joins /api/users; absolute overrides)
	// plus minimal-API MapGet/MapPost. /api prefix is stripped by the normalizer.
	for _, w := range []string{"GET /users/{}", "POST /users", "DELETE /absolute/{}", "GET /health", "POST /webhook"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	if len(producers) != 5 {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}

	// consumers — HttpClient; non-URL first arg skipped
	for _, w := range []string{"GET /products/{}", "POST /orders"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	if len(consumers) != 2 {
		t.Errorf("precision: unexpected consumers %v (cache.GetAsync should be skipped)", keys(consumers))
	}
}

// --- grpc ---

const grpcCS = `using Grpc.Core;
namespace shop
{
    public class CartServiceImpl : Hipstershop.CartService.CartServiceBase
    {
        public override void AddItem() {}
    }
    public class Caller {
        public void Connect() {
            var client = new Hipstershop.ShippingService.ShippingServiceClient(channel);
            var http = new HttpClient();
            var mismatch = new Foo.BarClient(channel);
        }
    }
}
`

func TestExtractCSharpGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grpc.cs"), []byte(grpcCS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	producers, consumers := map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindGRPCService:
			producers[n.Name] = true
		case graph.KindGRPCClient:
			consumers[n.Name] = true
		}
	}

	if !producers["CartService"] {
		t.Errorf("recall: <Svc>.<Svc>Base should register CartService producer (got %v)", keys(producers))
	}
	if !consumers["ShippingService"] {
		t.Errorf("recall: new <Svc>.<Svc>Client should register ShippingService consumer (got %v)", keys(consumers))
	}
	// precision — new HttpClient() and a mismatched Foo.BarClient are not gRPC.
	if consumers["Http"] || consumers["Bar"] || len(consumers) != 1 {
		t.Errorf("precision: non-gRPC ctor leaked (got %v)", keys(consumers))
	}
}

// --- config + sql ---

const cfgCS = `using System;
namespace shop {
    public class Startup {
        public IConfiguration Configuration { get; }
        public void Configure() {
            string redis = Configuration["REDIS_ADDR"];
            var port = Environment.GetEnvironmentVariable("PORT");
            var noise = other["NOT_CONFIG"];
            string sql = "SELECT id FROM orders WHERE user_id = 5";
            db.Execute(sql);
        }
    }
}
`

func TestExtractCSharpConfigAndSQL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Startup.cs"), []byte(cfgCS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	config, tables := map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindConfigKey:
			config[n.Name] = true
		case graph.KindDataEntity:
			tables[n.Name] = true
		}
	}

	for _, w := range []string{"REDIS_ADDR", "PORT"} {
		if !config[w] {
			t.Errorf("recall: config key %q missing (got %v)", w, keys(config))
		}
	}
	if config["NOT_CONFIG"] {
		t.Errorf("precision: other[\"NOT_CONFIG\"] is not a config read (got %v)", keys(config))
	}
	if !tables["orders"] {
		t.Errorf("recall: SQL table 'orders' missing from C# string literal (got %v)", keys(tables))
	}
}

// --- pubsub ---

const pubsubCS = `namespace shop {
    public class Mq {
        public void Work() {
            producer.ProduceAsync("orders.created", message);
            consumer.Subscribe("shipments.ready");
            events.Subscribe(handler);
        }
    }
}
`

func TestExtractCSharpPubSub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Mq.cs"), []byte(pubsubCS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	producers, consumers := map[string]bool{}, map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindTopicProducer:
			producers[n.Name] = true
		case graph.KindTopicConsumer:
			consumers[n.Name] = true
		}
	}

	if !producers["orders.created"] {
		t.Errorf("recall: ProduceAsync topic missing (got %v)", keys(producers))
	}
	if !consumers["shipments.ready"] {
		t.Errorf("recall: Subscribe topic missing (got %v)", keys(consumers))
	}
	// precision — events.Subscribe(handler) has a delegate, not a string topic.
	if len(consumers) != 1 {
		t.Errorf("precision: non-topic Subscribe leaked (got %v)", keys(consumers))
	}
}
