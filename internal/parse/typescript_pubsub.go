package parse

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractTSPubSub finds message-queue producers and consumers in TS/JS:
//
//	kafkajs: producer.send({ topic: "t", ... })     → producer
//	         consumer.subscribe({ topic: "t" })      → consumer
//	NATS.js: nc.publish("subj")                      → producer
//	         nc.subscribe("subj")                     → consumer
//
// kafkajs carries the topic as an object property (like Go's Kafka struct field);
// NATS carries it as the first positional string. Both key lowercased via topicKey.
func extractTSPubSub(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call_expression" {
			return
		}
		_, verb := tsCallTarget(p, n)
		args := n.ChildByFieldName("arguments")
		switch verb {
		case "publish": // NATS producer
			if subj, ok := p.tsFirstArgString(args); ok {
				p.addContract(graph.KindTopicProducer, topicKey(subj), map[string]string{"topic": subj})
			}
		case "send": // kafkajs producer: send({ topic: "t", ... })
			if topic, ok := p.tsFirstArgObjectTopic(args); ok {
				p.addContract(graph.KindTopicProducer, topicKey(topic), map[string]string{"topic": topic})
			}
		case "subscribe": // NATS ("subj") or kafkajs ({ topic: "t" }) consumer
			if subj, ok := p.tsFirstArgString(args); ok {
				p.addContract(graph.KindTopicConsumer, topicKey(subj), map[string]string{"topic": subj})
				return
			}
			if topic, ok := p.tsFirstArgObjectTopic(args); ok {
				p.addContract(graph.KindTopicConsumer, topicKey(topic), map[string]string{"topic": topic})
			}
		}
	})
}

// tsFirstArgObjectTopic reads the topic property off the first argument when it is
// an object literal: send({ topic: "payments" }) → ("payments", true).
func (p *fileCtx) tsFirstArgObjectTopic(args *sitter.Node) (string, bool) {
	if args == nil || args.NamedChildCount() == 0 {
		return "", false
	}
	return p.tsObjectStringProp(args.NamedChild(0), "topic")
}
