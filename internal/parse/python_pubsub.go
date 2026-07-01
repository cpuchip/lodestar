package parse

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractPythonPubSub finds message-queue producers and consumers in Python:
//
//	NATS:  nc.publish("subj", data)          → producer
//	       nc.subscribe("subj")              → consumer
//	Kafka: producer.send("topic", value=...) → producer
//	       KafkaConsumer("topic")            → consumer
//	       consumer.subscribe(["t1","t2"])   → consumer  (topics as a list)
//
// Topics/subjects are keyed lowercased via topicKey (shared with the Go extractor).
// Requiring a string-literal subject/topic naturally excludes dynamic subjects,
// keeping precision high; the resolve-time key-join is the cross-service safety net.
func extractPythonPubSub(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		if n.Type() != "call" {
			return
		}
		_, name := pyCallTarget(p, n)
		args := n.ChildByFieldName("arguments")
		switch name {
		case "publish": // NATS producer
			if subj, ok := p.pyFirstArgString(args); ok {
				p.addContract(graph.KindTopicProducer, topicKey(subj), map[string]string{"topic": subj})
			}
		case "send": // Kafka producer: send("topic", ...)
			if topic, ok := p.pyFirstArgString(args); ok {
				p.addContract(graph.KindTopicProducer, topicKey(topic), map[string]string{"topic": topic})
			}
		case "subscribe": // NATS ("subj") or Kafka (["t1","t2"]) consumer
			if subj, ok := p.pyFirstArgString(args); ok {
				p.addContract(graph.KindTopicConsumer, topicKey(subj), map[string]string{"topic": subj})
				return
			}
			if args != nil && args.NamedChildCount() > 0 && args.NamedChild(0).Type() == "list" {
				for _, t := range p.pyStringArgs(args.NamedChild(0)) {
					p.addContract(graph.KindTopicConsumer, topicKey(t), map[string]string{"topic": t})
				}
			}
		case "KafkaConsumer": // Kafka consumer ctor: KafkaConsumer("topic")
			if topic, ok := p.pyFirstArgString(args); ok {
				p.addContract(graph.KindTopicConsumer, topicKey(topic), map[string]string{"topic": topic})
			}
		}
	})
}
