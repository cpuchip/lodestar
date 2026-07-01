package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

const pubsubGo = `package svc

import (
	"github.com/nats-io/nats.go"
	kafka "github.com/segmentio/kafka-go"
)

func nats(nc *nats.Conn) {
	nc.Publish("orders.created", data)          // producer: orders.created
	nc.Subscribe("shipments.ready", handler)    // consumer: shipments.ready
	nc.QueueSubscribe("orders.created", "q", h) // consumer: orders.created
	redis.Publish(ctx, "ignored")               // ctx-first → not a string-first Publish, skipped
}

func kafkaSetup() {
	_ = kafka.NewWriter(kafka.WriterConfig{Topic: "PAYMENTS", Brokers: b}) // producer: payments (lowercased)
	_ = kafka.NewReader(kafka.ReaderConfig{Topic: "payments", GroupID: g}) // consumer: payments
}
`

// The pub/sub oracle: NATS Publish/Subscribe and Kafka Writer/Reader Topic fields
// surface as producers/consumers keyed by lowercased topic; a producer and a
// consumer on the same subject collide (orders.created, payments); redis.Publish
// with a ctx-first signature is not mistaken for a NATS publish.
func TestExtractGoPubSub(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mq.go"), []byte(pubsubGo), 0o644); err != nil {
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

	// recall — producers (NATS publish + kafka writer, lowercased)
	for _, w := range []string{"orders.created", "payments"} {
		if !producers[w] {
			t.Errorf("recall: missing producer %q (got %v)", w, keys(producers))
		}
	}
	// recall — consumers (NATS subscribe/queue-subscribe + kafka reader)
	for _, w := range []string{"shipments.ready", "orders.created", "payments"} {
		if !consumers[w] {
			t.Errorf("recall: missing consumer %q (got %v)", w, keys(consumers))
		}
	}
	// disambiguation — a producer and consumer meet on the same subject
	if !(producers["orders.created"] && consumers["orders.created"]) {
		t.Error("disambiguation: orders.created should be both producer and consumer")
	}
	// precision — the kafka Topic "PAYMENTS" was lowercased to match "payments"
	if producers["PAYMENTS"] {
		t.Error("precision: kafka topic should be lowercased")
	}
	// precision — redis.Publish(ctx, "ignored") is not a NATS publish (ctx-first)
	if producers["ignored"] {
		t.Error("precision: ctx-first Publish must not be read as a subject publish")
	}
}
