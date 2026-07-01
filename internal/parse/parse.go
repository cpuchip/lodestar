// Package parse is the structural layer: it walks a repo's source files and
// emits the skeleton — file / function / method / type nodes and the contains
// edges between them — using tree-sitter, config-driven per language.
//
// It is deliberately NOT a call graph. Per docs/ARCHITECTURE.md the value lives
// in the contract layer (HTTP/gRPC/pub-sub), which pairs services by normalized
// keys and needs no cross-file call resolution. The skeleton is what the
// contract extractors hang their producer/consumer nodes onto, and what a reader
// navigates. Deterministic by design: no LLM, same input → same graph.
package parse

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// Language is one language's extraction config: the file extensions it owns, the
// tree-sitter grammar, and an extractor that turns a parsed tree into nodes/edges.
// A new language is a new Language value — the grammars differ enough (Go's type
// name hangs off a nested type_spec; Python has no type decls) that a shared
// config DSL would guess wrong. Shared walking helpers live below.
type Language struct {
	Name    string
	Exts    []string
	grammar *sitter.Language
	extract func(p *fileCtx, root *sitter.Node)
	// contracts are the per-protocol extractors (HTTP/gRPC/pub-sub) run after the
	// structural pass. Each walks the same tree for its producer/consumer patterns.
	contracts []func(p *fileCtx, root *sitter.Node)
}

// fileCtx carries everything an extractor needs for one file.
type fileCtx struct {
	world  string // the service / repo
	rel    string // repo-relative path (forward slashes)
	src    []byte
	fileID string
	g      *graph.Graph
	seen   map[string]bool // node IDs already emitted for this file (dedup)
}

// Languages returns the configured language set (V1: Go).
func Languages() []Language {
	return []Language{goLanguage()}
}

// langForPath returns the Language that owns a file, or nil.
func langForPath(langs []Language, path string) *Language {
	ext := strings.ToLower(filepath.Ext(path))
	for i := range langs {
		for _, e := range langs[i].Exts {
			if e == ext {
				return &langs[i]
			}
		}
	}
	return nil
}

// ParseDir walks dir, parses every file a configured language owns, and returns
// the structural graph for one world (service/repo). World is the logical name
// of the service this directory IS (e.g. "checkout-service").
func ParseDir(world, dir string) (*graph.Graph, error) {
	langs := Languages()
	g := &graph.Graph{Worlds: []string{world}}

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		lang := langForPath(langs, path)
		if lang == nil {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		return parseFile(g, world, filepath.ToSlash(rel), path, lang)
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// parseFile parses one source file into g.
func parseFile(g *graph.Graph, world, rel, absPath string, lang *Language) error {
	src, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	parser := sitter.NewParser()
	parser.SetLanguage(lang.grammar)
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return err
	}
	defer tree.Close()

	fileID := world + "::" + rel
	g.Nodes = append(g.Nodes, graph.Node{
		ID:    fileID,
		World: world,
		Kind:  graph.KindFile,
		Name:  rel,
	})
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g, seen: map[string]bool{fileID: true}}
	root := tree.RootNode()
	lang.extract(p, root)
	for _, extractContracts := range lang.contracts {
		extractContracts(p, root)
	}
	return nil
}

// addDecl appends a declaration node (function/method/type/...) plus the
// file-contains-decl edge, and returns the new node's ID.
func (p *fileCtx) addDecl(kind, name string, meta map[string]string) string {
	return p.addNode(p.fileID+"::"+name, kind, name, meta)
}

// addContract appends a producer/consumer contract node (http_endpoint,
// http_client, grpc_method, topic_producer, ...) keyed by its normalized contract
// key, plus the file-contains edge. Name IS the key so the pairing at resolve time
// (and the substrate's (world,kind,name) matcher) agree on identity; two routes
// that normalize to the same key in one file collapse to one node.
func (p *fileCtx) addContract(kind, key string, meta map[string]string) {
	p.addNode(p.fileID+"::"+kind+"::"+key, kind, key, meta)
}

// addNode is the shared emit: dedups by ID within the file, appends the node and
// its file-contains edge.
func (p *fileCtx) addNode(id, kind, name string, meta map[string]string) string {
	if p.seen[id] {
		return id
	}
	p.seen[id] = true
	p.g.Nodes = append(p.g.Nodes, graph.Node{
		ID:       id,
		World:    p.world,
		Kind:     kind,
		Name:     name,
		Metadata: meta,
	})
	p.g.Edges = append(p.g.Edges, graph.Edge{Src: p.fileID, Dst: id, Rel: graph.RelContains})
	return id
}

// walk visits n and every descendant in pre-order.
func walk(n *sitter.Node, fn func(*sitter.Node)) {
	fn(n)
	for i := 0; i < int(n.NamedChildCount()); i++ {
		walk(n.NamedChild(i), fn)
	}
}

// stringLit returns the unquoted value of a Go string-literal node, or ("",false).
func (p *fileCtx) stringLit(n *sitter.Node) (string, bool) {
	switch n.Type() {
	case "interpreted_string_literal", "raw_string_literal":
		return strings.Trim(n.Content(p.src), "\"`"), true
	}
	return "", false
}

// stringArgs returns, in order, the string-literal arguments of an argument_list
// (non-literal args like handlers, contexts, and bodies are skipped).
func (p *fileCtx) stringArgs(argList *sitter.Node) []string {
	var out []string
	if argList == nil {
		return out
	}
	for i := 0; i < int(argList.NamedChildCount()); i++ {
		if s, ok := p.stringLit(argList.NamedChild(i)); ok {
			out = append(out, s)
		}
	}
	return out
}

// skipDir filters out vendored / VCS / dependency directories that would bloat
// the graph with code the service doesn't own.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".idea", ".vscode", "dist", "build", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".") && len(name) > 1 && name != ".github"
}

// --- shared tree-sitter helpers ---

// fieldText returns the source text of a node's named field, or "".
func (p *fileCtx) fieldText(n *sitter.Node, field string) string {
	c := n.ChildByFieldName(field)
	if c == nil {
		return ""
	}
	return c.Content(p.src)
}

// namedChildrenOfType returns a node's direct named children of a given type.
func namedChildrenOfType(n *sitter.Node, typ string) []*sitter.Node {
	var out []*sitter.Node
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == typ {
			out = append(out, c)
		}
	}
	return out
}
