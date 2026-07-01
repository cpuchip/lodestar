package parse

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// extractGoPubSub finds message-queue producers and consumers in Go:
//
//	NATS:  nc.Publish("subj", data)           → producer
//	       nc.Subscribe("subj", h) / QueueSubscribe / ChanSubscribe → consumer
//	Kafka: kafka.WriterConfig{Topic: "t"} / sarama.ProducerMessage{Topic:"t"} → producer
//	       kafka.ReaderConfig{Topic: "t"}     → consumer
//	       consumer.ConsumePartition("t", ...) → consumer  (sarama)
//
// Topics/subjects are keyed lowercased (matching the substrate's pub-sub resolver).
// Requiring a string-LITERAL argument for the call forms naturally excludes
// GCP/Redis Publish(ctx, ...) (ctx is not a string), keeping precision high.
func extractGoPubSub(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "call_expression":
			_, verb := goCallTarget(p, n)
			// The subject/topic must be the FIRST positional argument and a string
			// literal (so redis rdb.Publish(ctx, "chan", ...) — ctx-first — is skipped).
			subj, ok := p.firstArgString(n.ChildByFieldName("arguments"))
			if !ok {
				return
			}
			switch verb {
			case "Publish":
				p.addContract(graph.KindTopicProducer, topicKey(subj), map[string]string{"topic": subj})
			case "Subscribe", "QueueSubscribe", "SubscribeSync", "ChanSubscribe", "ChanQueueSubscribe", "ConsumePartition":
				p.addContract(graph.KindTopicConsumer, topicKey(subj), map[string]string{"topic": subj})
			}
		case "composite_literal":
			kind := kafkaKindFromType(compositeTypeName(p, n))
			if kind == "" {
				return
			}
			if topic, ok := p.topicField(n); ok {
				p.addContract(kind, topicKey(topic), map[string]string{"topic": topic})
			}
		}
	})
}

// topicKey normalizes a topic/subject name (lowercased), matching the substrate.
func topicKey(topic string) string { return strings.ToLower(topic) }

// kafkaKindFromType classifies a composite-literal type name (WriterConfig,
// ReaderConfig, ProducerMessage, ...) into a producer/consumer kind, or "".
func kafkaKindFromType(typ string) string {
	switch {
	case strings.Contains(typ, "Writer"), strings.Contains(typ, "Producer"):
		return graph.KindTopicProducer
	case strings.Contains(typ, "Reader"), strings.Contains(typ, "Consumer"), strings.Contains(typ, "Subscriber"):
		return graph.KindTopicConsumer
	}
	return ""
}

// compositeTypeName returns the (unqualified) type name of a composite_literal:
// kafka.WriterConfig{...} → "WriterConfig", ReaderConfig{...} → "ReaderConfig".
func compositeTypeName(p *fileCtx, n *sitter.Node) string {
	t := n.ChildByFieldName("type")
	if t == nil && n.NamedChildCount() > 0 {
		t = n.NamedChild(0)
	}
	if t == nil {
		return ""
	}
	if t.Type() == "type_identifier" {
		return t.Content(p.src)
	}
	// qualified_type (pkg.Type) or otherwise — take the last type_identifier within.
	for i := int(t.NamedChildCount()) - 1; i >= 0; i-- {
		if t.NamedChild(i).Type() == "type_identifier" {
			return t.NamedChild(i).Content(p.src)
		}
	}
	return t.Content(p.src)
}

// topicField pulls a string-literal `Topic:` field out of a composite literal's body.
func (p *fileCtx) topicField(n *sitter.Node) (string, bool) {
	var lv *sitter.Node
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if n.NamedChild(i).Type() == "literal_value" {
			lv = n.NamedChild(i)
			break
		}
	}
	if lv == nil {
		return "", false
	}
	for _, ke := range namedChildrenOfType(lv, "keyed_element") {
		els := namedChildrenOfType(ke, "literal_element")
		if len(els) < 2 {
			continue
		}
		if key := els[0].Content(p.src); key != "Topic" && key != "topic" {
			continue
		}
		// value element wraps the string literal
		if s, ok := p.stringLit(els[1]); ok {
			return s, true
		}
		if els[1].NamedChildCount() > 0 {
			if s, ok := p.stringLit(els[1].NamedChild(0)); ok {
				return s, true
			}
		}
	}
	return "", false
}
