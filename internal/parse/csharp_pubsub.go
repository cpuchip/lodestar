package parse

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractCSharpPubSub finds Kafka producers and consumers in C# (Confluent.Kafka),
// keyed lowercased to match the pub/sub resolver and the other languages:
//
//	producer.ProduceAsync("topic", message)  → producer
//	consumer.Subscribe("topic")               → consumer
//
// Both require a string-LITERAL first argument. That gate makes even the generic
// method name Subscribe safe: Rx/event Subscribe takes a delegate, not a string,
// so only the Kafka topic form matches. Precision over recall — a dynamic topic is
// skipped.
func extractCSharpPubSub(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "invocation_expression" {
			return
		}
		_, name := csCallTarget(p, n)
		topic, ok := p.csFirstArgString(n.ChildByFieldName("arguments"))
		if !ok || topic == "" {
			return
		}
		switch name {
		case "ProduceAsync", "Produce":
			p.addContract(graph.KindTopicProducer, topicKey(topic), map[string]string{"topic": topic})
		case "Subscribe":
			p.addContract(graph.KindTopicConsumer, topicKey(topic), map[string]string{"topic": topic})
		}
	})
}
