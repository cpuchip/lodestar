package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// --- structural ---

const sampleJava = `package hipstershop;
import io.grpc.Server;
import io.grpc.stub.StreamObserver;

class BaseService {}
interface Worker {}

public class AdService extends BaseService implements Worker {
  public void start() { helper(); }
  private void helper() {}
  static class Impl {
    public void run() {}
  }
}
enum Color { RED, GREEN }
`

func TestParseJavaSkeleton(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AdService.java"), []byte(sampleJava), 0o644); err != nil {
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

	// recall — every declaration (incl. the nested Impl class + its method)
	want := []struct{ kind, name string }{
		{graph.KindFile, "AdService.java"},
		{graph.KindClass, "BaseService"},
		{graph.KindInterface, "Worker"},
		{graph.KindClass, "AdService"},
		{graph.KindMethod, "AdService.start"},
		{graph.KindMethod, "AdService.helper"},
		{graph.KindClass, "Impl"},
		{graph.KindMethod, "Impl.run"},
		{graph.KindClass, "Color"},
	}
	for _, w := range want {
		if _, ok := byKindName[w.kind+" "+w.name]; !ok {
			t.Errorf("recall: missing %s %q", w.kind, w.name)
		}
	}

	// precision — exactly these structural nodes (this sample triggers no contracts)
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

	// discrimination — receiver recorded, same method name stays distinct
	if m := byKindName[graph.KindMethod+" AdService.start"]; m.Metadata["receiver"] != "AdService" {
		t.Errorf("discrimination: AdService.start receiver = %q, want AdService", m.Metadata["receiver"])
	}

	// imports as file metadata
	if file := byKindName[graph.KindFile+" AdService.java"]; file.Metadata["imports"] != "io.grpc.Server io.grpc.stub.StreamObserver" {
		t.Errorf("imports: file metadata = %q", file.Metadata["imports"])
	}

	// deep call graph — inherits, implements, and an intra-world call resolve
	edges := map[string]bool{}
	for _, e := range g.Edges {
		if e.Rel != graph.RelContains {
			edges[e.Rel+" "+e.Src+" -> "+e.Dst] = true
		}
	}
	base := "svc::AdService.java::"
	wantEdges := []string{
		graph.RelInherits + " " + base + "AdService -> " + base + "BaseService",
		graph.RelImplements + " " + base + "AdService -> " + base + "Worker",
		graph.RelCalls + " " + base + "AdService.start -> " + base + "AdService.helper",
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

const httpJava = `package shop;
import org.springframework.web.bind.annotation.*;
import org.springframework.web.client.RestTemplate;

@RestController
@RequestMapping("/users")
class UserController {
  @GetMapping("/{id}")
  public User get(String id) { return null; }

  @PostMapping
  public void create() {}

  @RequestMapping(value="/legacy", method=RequestMethod.PUT)
  public void legacy() {}

  public void callOut() {
    restTemplate.getForObject("http://catalog/products/42", String.class);
    restTemplate.postForEntity("http://catalog/orders", body, String.class);
    restTemplate.exchange("http://catalog/items/7", HttpMethod.DELETE, entity, String.class);
    cache.getForObject("not-a-url", String.class);
  }
}
`

func TestExtractJavaHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "UserController.java"), []byte(httpJava), 0o644); err != nil {
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

	// producers — class-level @RequestMapping base prefixed onto each method
	for _, w := range []string{"GET /users/{}", "POST /users", "PUT /users/legacy"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	if len(producers) != 3 {
		t.Errorf("precision: unexpected producers %v", keys(producers))
	}

	// consumers — RestTemplate, incl. exchange's HttpMethod arg; non-URL skipped
	for _, w := range []string{"GET /products/{}", "POST /orders", "DELETE /items/{}"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	if len(consumers) != 3 {
		t.Errorf("precision: unexpected consumers %v (cache.getForObject should be skipped)", keys(consumers))
	}
}

// --- grpc ---

const grpcJava = `package hipstershop;
class AdServiceImpl extends hipstershop.AdServiceGrpc.AdServiceImplBase {
  public void getAds() {}
}
class Client {
  void connect() {
    var stub = hipstershop.AdServiceGrpc.newBlockingStub(channel);
    var other = ShippingServiceGrpc.newStub(channel);
    var redis = RedisGrpc.newBlockingStub(channel);
  }
}
`

func TestExtractJavaGRPC(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grpc.java"), []byte(grpcJava), 0o644); err != nil {
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

	if !producers["AdService"] {
		t.Errorf("recall: AdServiceImplBase should register AdService producer (got %v)", keys(producers))
	}
	for _, w := range []string{"AdService", "ShippingService"} {
		if !consumers[w] {
			t.Errorf("recall: missing gRPC consumer %q (got %v)", w, keys(consumers))
		}
	}
	if consumers["Redis"] {
		t.Errorf("precision: denylisted RedisGrpc stub leaked (got %v)", keys(consumers))
	}
}

// --- config + sql ---

const cfgJava = `package shop;
class Cfg {
  void load() {
    String a = System.getenv("REDIS_ADDR");
    String b = System.getenv("PORT");
    int x = compute("NOT_ENV");
    String sql = "SELECT id, name FROM customers WHERE id = 1";
    db.execute(sql);
  }
}
`

func TestExtractJavaConfigAndSQL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cfg.java"), []byte(cfgJava), 0o644); err != nil {
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

	if !config["REDIS_ADDR"] {
		t.Errorf("recall: System.getenv(\"REDIS_ADDR\") missing (got %v)", keys(config))
	}
	if config["NOT_ENV"] {
		t.Errorf("precision: compute(\"NOT_ENV\") is not an env read (got %v)", keys(config))
	}
	if !tables["customers"] {
		t.Errorf("recall: SQL table 'customers' missing from Java string literal (got %v)", keys(tables))
	}
}

// --- pubsub ---

const pubsubJava = `package shop;
class Mq {
  void send() {
    producer.send(new ProducerRecord("orders.created", data));
    producer.send("not-a-record", data);
  }
  @KafkaListener(topics = "shipments.ready")
  public void listen() {}
  @KafkaListener(topics = {"events.a", "events.b"})
  public void listen2() {}
}
`

func TestExtractJavaPubSub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Mq.java"), []byte(pubsubJava), 0o644); err != nil {
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
		t.Errorf("recall: ProducerRecord topic missing (got %v)", keys(producers))
	}
	if len(producers) != 1 {
		t.Errorf("precision: bare send(\"string\") must not be a producer (got %v)", keys(producers))
	}
	for _, w := range []string{"shipments.ready", "events.a", "events.b"} {
		if !consumers[w] {
			t.Errorf("recall: @KafkaListener topic %q missing (got %v)", w, keys(consumers))
		}
	}
}
