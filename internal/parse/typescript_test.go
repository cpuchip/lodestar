package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// --- structural ---

const sampleTS = `import { foo } from "./foo";
import axios from "axios";

interface Widget {
  id: number;
}

export class Server {
  start(): void {}
  stop(): void {}
}

class Handler {
  serve(): void {}
}

export function makeServer(addr: string): void {}

function helper(): void {}
`

func TestParseTSSkeleton(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.ts"), []byte(sampleTS), 0o644); err != nil {
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

	// recall — every declaration, with the right kind (exported ones unwrapped)
	want := []struct{ kind, name string }{
		{graph.KindFile, "server.ts"},
		{graph.KindInterface, "Widget"},
		{graph.KindClass, "Server"},
		{graph.KindMethod, "Server.start"},
		{graph.KindMethod, "Server.stop"},
		{graph.KindClass, "Handler"},
		{graph.KindMethod, "Handler.serve"},
		{graph.KindFunction, "makeServer"},
		{graph.KindFunction, "helper"},
	}
	for _, w := range want {
		if _, ok := byKindName[w.kind+" "+w.name]; !ok {
			t.Errorf("recall: missing %s %q", w.kind, w.name)
		}
	}

	// precision — exactly these nodes
	if len(g.Nodes) != len(want) {
		var got []string
		for _, n := range g.Nodes {
			got = append(got, n.Kind+" "+n.Name)
		}
		t.Errorf("precision: got %d nodes, want %d: %v", len(g.Nodes), len(want), got)
	}

	// discrimination — a method is receiver-qualified
	if m := byKindName[graph.KindMethod+" Server.start"]; m.Metadata["receiver"] != "Server" {
		t.Errorf("discrimination: Server.start receiver = %q, want Server", m.Metadata["receiver"])
	}

	// structure — every non-file node is contained by the file
	fileID := "svc::server.ts"
	contained := map[string]bool{}
	for _, e := range g.Edges {
		if e.Rel == graph.RelContains && e.Src == fileID {
			contained[e.Dst] = true
		}
	}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindFile {
			continue
		}
		if !contained[n.ID] {
			t.Errorf("structure: %s %q not contained by its file", n.Kind, n.Name)
		}
	}

	// imports captured as file metadata (module specifiers)
	if file := byKindName[graph.KindFile+" server.ts"]; file.Metadata["imports"] != "./foo axios" {
		t.Errorf("imports: file metadata = %q, want %q", file.Metadata["imports"], "./foo axios")
	}
}

// TestParseJSAlsoWorks proves the shared extractor is wired for the javascript
// grammar too (a .js file yields the same skeleton kinds, no interface).
func TestParseJSAlsoWorks(t *testing.T) {
	dir := t.TempDir()
	js := "export class Client {\n  send() {}\n}\nfunction run() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range g.Nodes {
		got[n.Kind+" "+n.Name] = true
	}
	for _, w := range []string{
		graph.KindClass + " Client",
		graph.KindMethod + " Client.send",
		graph.KindFunction + " run",
	} {
		if !got[w] {
			t.Errorf(".js recall: missing %q (got %v)", w, keys(got))
		}
	}
}

// --- http ---

const httpTS = `function routes(app, router) {
  app.get("/products/:id", getProduct);
  router.post("/orders", makeOrder);
  app.get(dynamicPath, h);          // dynamic path — skipped
}

function calls() {
  fetch("http://catalog/products/42");
  axios.post("http://catalog/products");
  axios("http://x/items/7");
  http.get("/users/7");
}
`

func TestExtractTSHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http.ts"), []byte(httpTS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

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

	// recall — Express routes
	for _, w := range []string{"GET /products/{}", "POST /orders"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	// recall — fetch / axios / http.get consumers
	for _, w := range []string{"GET /products/{}", "POST /products", "GET /items/{}", "GET /users/{}"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	// disambiguation — the app.get route pairs with the fetch call
	if !(producers["GET /products/{}"] && consumers["GET /products/{}"]) {
		t.Error("disambiguation: GET /products/{} should be BOTH a producer and a consumer")
	}
	// precision — dynamic route skipped; exactly the two routes
	if len(producers) != 2 {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}
}

// --- grpc ---

const grpcTS = `function grpc(addr, creds, opts) {
  const a = new ShippingServiceClient(addr, creds);
  const b = new RedisClient(opts);                                   // denylisted
  const c = new proto.demo.ProductCatalogServiceClient(addr, creds); // member ctor
}
`

func TestExtractTSGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grpc.ts"), []byte(grpcTS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	consumers := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindGRPCClient {
			consumers[n.Name] = true
		}
	}

	for _, w := range []string{"ShippingService", "ProductCatalogService"} {
		if !consumers[w] {
			t.Errorf("recall: missing gRPC consumer %q (got %v)", w, keys(consumers))
		}
	}
	// precision — a denylisted ctor does not leak
	if consumers["Redis"] || consumers[""] {
		t.Errorf("precision: non-gRPC client ctor leaked: %v", keys(consumers))
	}
}

// --- pubsub ---

const pubsubTS = `async function mq(producer, consumer, nc) {
  await producer.send({ topic: "payments", messages: [] });
  await consumer.subscribe({ topic: "audit", fromBeginning: true });
  nc.publish("orders.created", data);
  nc.subscribe("shipments.ready");
}
`

func TestExtractTSPubSub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mq.ts"), []byte(pubsubTS), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}

	producers := map[string]bool{}
	consumers := map[string]bool{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindTopicProducer:
			producers[n.Name] = true
		case graph.KindTopicConsumer:
			consumers[n.Name] = true
		}
	}

	// recall — kafkajs object-topic + NATS positional-subject
	for _, w := range []string{"payments", "orders.created"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	for _, w := range []string{"audit", "shipments.ready"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	// precision — no stray producers/consumers
	if len(producers) != 2 || len(consumers) != 2 {
		t.Errorf("precision: producers=%v consumers=%v", keys(producers), keys(consumers))
	}
}
