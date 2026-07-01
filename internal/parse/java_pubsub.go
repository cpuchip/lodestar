package parse

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractJavaPubSub finds Kafka producers and consumers in Java, keyed lowercased
// (matching the pub/sub resolver and the other languages):
//
//	producer.send(new ProducerRecord("topic", ...))  → producer   (kafka-clients)
//	@KafkaListener(topics = "topic" | {"a","b"})       → consumer   (spring-kafka)
//
// Scope is deliberately narrow for precision. The kafka-clients producer keys on
// the unambiguous ProducerRecord constructor's first string argument; the Spring
// consumer keys on the @KafkaListener topics attribute.
//
// DEFERRED: the Spring KafkaTemplate producer form kafkaTemplate.send("topic",
// data) — `send` with a bare string first arg is far too common a method name
// across unrelated APIs to key on without false positives; the ProducerRecord form
// carries a distinctive, unambiguous token. A missed producer is preferable to a
// wrong topic edge (precision over recall).
func extractJavaPubSub(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "method_invocation":
			_, name := javaCallTarget(p, n)
			if name != "send" {
				return
			}
			args := n.ChildByFieldName("arguments")
			if args == nil || args.NamedChildCount() == 0 {
				return
			}
			first := args.NamedChild(0)
			if first.Type() != "object_creation_expression" {
				return
			}
			if topic, ok := p.javaProducerRecordTopic(first); ok {
				p.addContract(graph.KindTopicProducer, topicKey(topic), map[string]string{"topic": topic})
			}
		case "annotation":
			if p.javaAnnotationName(n) != "KafkaListener" {
				return
			}
			for _, t := range p.javaAnnotationValues(n, "topics") {
				p.addContract(graph.KindTopicConsumer, topicKey(t), map[string]string{"topic": t})
			}
		}
	})
}

// javaProducerRecordTopic pulls the topic (first string arg) out of a
// `new ProducerRecord("topic", ...)` object_creation_expression.
func (p *fileCtx) javaProducerRecordTopic(n *sitter.Node) (string, bool) {
	t := n.ChildByFieldName("type")
	if t == nil {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			switch c := n.NamedChild(i); c.Type() {
			case "type_identifier", "scoped_type_identifier", "generic_type":
				t = c
			}
			if t != nil {
				break
			}
		}
	}
	if t == nil || javaTypeName(p, t) != "ProducerRecord" {
		return "", false
	}
	args := n.ChildByFieldName("arguments")
	if args == nil {
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c.Type() == "argument_list" {
				args = c
				break
			}
		}
	}
	return p.javaFirstArgString(args)
}
