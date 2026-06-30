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
}

// fileCtx carries everything an extractor needs for one file.
type fileCtx struct {
	world   string // the service / repo
	rel     string // repo-relative path (forward slashes)
	src     []byte
	fileID  string
	g       *graph.Graph
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
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g}
	lang.extract(p, tree.RootNode())
	return nil
}

// addDecl appends a declaration node (function/method/type/...) plus the
// file-contains-decl edge, and returns the new node's ID.
func (p *fileCtx) addDecl(kind, name string, meta map[string]string) string {
	id := p.fileID + "::" + name
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
