package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// --- structural ---

const samplePy = `import os
from mypkg.sub import thing

class Server:
    def start(self):
        pass

    def stop(self):
        pass

class Handler:
    def serve(self):
        pass

def make_server(addr):
    pass
`

func TestParsePythonSkeleton(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.py"), []byte(samplePy), 0o644); err != nil {
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

	// recall — every declaration, with the right kind
	want := []struct{ kind, name string }{
		{graph.KindFile, "server.py"},
		{graph.KindClass, "Server"},
		{graph.KindMethod, "Server.start"},
		{graph.KindMethod, "Server.stop"},
		{graph.KindClass, "Handler"},
		{graph.KindMethod, "Handler.serve"},
		{graph.KindFunction, "make_server"},
	}
	for _, w := range want {
		if _, ok := byKindName[w.kind+" "+w.name]; !ok {
			t.Errorf("recall: missing %s %q", w.kind, w.name)
		}
	}

	// precision — exactly these nodes (no bogus decls from call/body expressions)
	if len(g.Nodes) != len(want) {
		var got []string
		for _, n := range g.Nodes {
			got = append(got, n.Kind+" "+n.Name)
		}
		t.Errorf("precision: got %d nodes, want %d: %v", len(g.Nodes), len(want), got)
	}

	// discrimination — same method name on two classes stays distinct
	if m := byKindName[graph.KindMethod+" Server.start"]; m.Metadata["receiver"] != "Server" {
		t.Errorf("discrimination: Server.start receiver = %q, want Server", m.Metadata["receiver"])
	}

	// structure — every non-file node is contained by the file
	fileID := "svc::server.py"
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

	// imports captured as file metadata (module names, not symbols)
	if file := byKindName[graph.KindFile+" server.py"]; file.Metadata["imports"] != "os mypkg.sub" {
		t.Errorf("imports: file metadata = %q, want %q", file.Metadata["imports"], "os mypkg.sub")
	}
}

// --- http ---

const httpPy = `from fastapi import FastAPI
import requests

app = FastAPI()
router = APIRouter()

@app.get("/products/{id}")
def get_product(id):
    pass

@router.post("/orders")
def make_order():
    pass

@app.route("/legacy", methods=["POST"])
def legacy():
    pass

@app.route("/plain")
def plain():
    pass

def calls():
    requests.get("http://catalog/products/42")
    httpx.post("http://catalog/products")
    session.get("/users/7")
    r = requests.get(dynamic_url)   # dynamic path — skipped
    cache.get("some-cache-key")     # not a client call — skipped
`

func TestExtractPythonHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "http.py"), []byte(httpPy), 0o644); err != nil {
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

	// recall — producers, keyed and normalized
	wantProducers := []string{
		"GET /products/{}", // FastAPI @app.get, {id}->{}
		"POST /orders",     // @router.post
		"POST /legacy",     // Flask @app.route methods=["POST"]
		"GET /plain",       // Flask @app.route, default GET
	}
	for _, w := range wantProducers {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}

	// recall — consumers
	for _, w := range []string{"GET /products/{}", "POST /products", "GET /users/{}"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}

	// disambiguation — producer and consumer collide on GET /products/{}
	if !(producers["GET /products/{}"] && consumers["GET /products/{}"]) {
		t.Error("disambiguation: GET /products/{} should be BOTH a producer and a consumer")
	}

	// precision — dynamic path + non-client cache.get were skipped; exactly 4 routes
	if len(producers) != len(wantProducers) {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}
	if consumers["GET /products"] {
		t.Error("precision: POST /products must not read as GET /products")
	}
}

// --- grpc ---

const grpcPy = `import demo_pb2_grpc

def serve(server, servicer):
    demo_pb2_grpc.add_ProductCatalogServiceServicer_to_server(servicer, server)

def clients(channel):
    a = shipping_pb2_grpc.ShippingServiceStub(channel)
    b = cache.RedisStub(channel)                # Redis is denylisted — skipped
    ch = grpc.insecure_channel("localhost:50051") # not a stub/registrar
`

func TestExtractPythonGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grpc.py"), []byte(grpcPy), 0o644); err != nil {
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
		case graph.KindGRPCService:
			producers[n.Name] = true
		case graph.KindGRPCClient:
			consumers[n.Name] = true
		}
	}

	if !producers["ProductCatalogService"] {
		t.Errorf("recall: missing gRPC producer ProductCatalogService (got %v)", keys(producers))
	}
	if !consumers["ShippingService"] {
		t.Errorf("recall: missing gRPC consumer ShippingService (got %v)", keys(consumers))
	}
	// precision — denylisted / non-gRPC ctors do not leak
	if consumers["Redis"] || consumers[""] {
		t.Errorf("precision: non-gRPC stub leaked: %v", keys(consumers))
	}
}

// --- pubsub ---

const pubsubPy = `def nats(nc):
    nc.publish("orders.created", data)
    nc.subscribe("shipments.ready")
    nc.publish(dynamic_subj)                     # dynamic subject — skipped

def kafka():
    producer.send("PAYMENTS", value=b"x")        # producer: payments (lowercased)
    consumer = KafkaConsumer("payments")          # consumer: payments
    consumer.subscribe(["events.a", "events.b"])  # consumer: two topics
`

func TestExtractPythonPubSub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mq.py"), []byte(pubsubPy), 0o644); err != nil {
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

	for _, w := range []string{"orders.created", "payments"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	for _, w := range []string{"shipments.ready", "payments", "events.a", "events.b"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	// disambiguation — payments is both produced and consumed
	if !(producers["payments"] && consumers["payments"]) {
		t.Error("disambiguation: payments should be both producer and consumer")
	}
	// precision — topic lowercased; dynamic subject skipped
	if producers["PAYMENTS"] {
		t.Error("precision: kafka topic should be lowercased")
	}
	if producers["orders.created"] && len(producers) != 2 {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}
}
