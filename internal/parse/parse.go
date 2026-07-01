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
	// refs is the WORLD-level accumulator for the deep call graph: a call/base
	// target can be defined in another file of the same world, so references are
	// collected during Pass 1 (structural extraction, per file) and resolved to
	// edges in Pass 2 (after every file of the world is parsed). It points at a
	// single slice shared by every fileCtx of one ParseDir run.
	refs *[]pendingRef
}

// pendingRef is a call / inherit / implement reference recorded during structural
// extraction (Pass 1) and resolved after the whole world is parsed (Pass 2). It
// deliberately holds only the ENCLOSING decl's already-emitted node ID and the
// bare target NAME: the target may be defined in another file of the same world,
// so it cannot be resolved to an ID until every file has been walked.
type pendingRef struct {
	srcID  string // enclosing function/method/class node ID (already emitted)
	target string // bare callee / base name to resolve against the world's symbols
	rel    string // graph.RelCalls | RelInherits | RelImplements
}

// Languages returns the configured language set.
func Languages() []Language {
	return []Language{goLanguage(), protoLanguage(), pythonLanguage(), tsLanguage(), jsLanguage(), javaLanguage(), csharpLanguage()}
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
	var pending []pendingRef // Pass-1 accumulator, resolved in Pass 2 below

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
		if skipFile(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		// OpenAPI/Swagger specs are structured data, not code — handle them on a
		// parallel path (checked before langForPath, since no Language owns .yaml/
		// .json). A spec's endpoints key via the same NormalizeHTTPKey as code
		// routes, so they pair with code clients at resolve time for free.
		if isOpenAPISpec(path, d.Name()) {
			return parseOpenAPI(g, world, filepath.ToSlash(rel), path)
		}
		// Helm charts (structured YAML): the chart's service producer + the values'
		// upstream service references. Checked by exact filename before langForPath,
		// since no Language owns .yaml.
		if isHelmChartFile(d.Name()) {
			return parseHelmChart(g, world, filepath.ToSlash(rel), path)
		}
		if isHelmValuesFile(d.Name()) {
			return parseHelmValues(g, world, filepath.ToSlash(rel), path)
		}
		lang := langForPath(langs, path)
		if lang == nil {
			return nil
		}
		return parseFile(g, &pending, world, filepath.ToSlash(rel), path, lang)
	})
	if err != nil {
		return nil, err
	}
	// Pass 2: now that every declaration in the world is known, resolve the deep
	// call graph (calls / inherits / implements) into edges. This must happen after
	// the file loop because a call or base class can live in a different file.
	resolveRefs(g, world, pending)
	return g, nil
}

// parseFile parses one source file into g. pending is the world-level reference
// accumulator threaded through so extractors can record calls/inherits/implements
// for Pass 2 resolution.
func parseFile(g *graph.Graph, pending *[]pendingRef, world, rel, absPath string, lang *Language) error {
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
	p := &fileCtx{world: world, rel: rel, src: src, fileID: fileID, g: g, seen: map[string]bool{fileID: true}, refs: pending}
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

// --- deep call graph: Pass 1 recording, Pass 2 resolution ---

// addRef records a pending reference (no-op for an empty src/target).
func (p *fileCtx) addRef(srcID, target, rel string) {
	if srcID == "" || target == "" {
		return
	}
	*p.refs = append(*p.refs, pendingRef{srcID: srcID, target: target, rel: rel})
}

// recordCalls walks the subtree rooted at body and records a pending calls ref
// from srcID for every call node (of type callType) whose bare callee nameFn can
// name. Nested calls — in argument positions, in closures — attribute to the same
// enclosing decl, since closures are not their own nodes in the skeleton.
// Resolution to an actual node happens in Pass 2; a stdlib/third-party/dynamic
// callee simply won't match a world symbol and yields no edge (precision > recall).
func (p *fileCtx) recordCalls(srcID string, body *sitter.Node, callType string, nameFn func(*sitter.Node) string) {
	if srcID == "" || body == nil {
		return
	}
	walk(body, func(n *sitter.Node) {
		if n.Type() != callType {
			return
		}
		if name := nameFn(n); name != "" {
			p.addRef(srcID, name, graph.RelCalls)
		}
	})
}

// resolveRefs is Pass 2: it turns pending call/inherit/implement references into
// edges, but ONLY when a reference's bare name resolves to EXACTLY ONE
// kind-appropriate declaration in the world. This is precision over recall by
// construction — an unresolved name (stdlib, third-party, dynamic) or an ambiguous
// one (two defs sharing the name) yields NO edge; a missing call edge is fine, a
// wrong one is not. Resolution is intra-world only: the symbol table is built from
// this world's nodes, so a resolved edge can never cross a world boundary (that is
// the contract layer's job). Deterministic: table and pending are both traversed
// in slice order, so identical input yields an identical edge sequence.
func resolveRefs(g *graph.Graph, world string, pending []pendingRef) {
	if len(pending) == 0 {
		return
	}
	type symbol struct{ id, kind string }
	// Symbol table: lookup-name -> matching decls. Functions/classes/interfaces key
	// on their (bare) name; methods key on the bare method name (last dotted
	// segment) because a call site (`s.setup()`) sees only the method name, not the
	// receiver type — V1 receiver-less resolution, emitted on a unique match only.
	table := map[string][]symbol{}
	for _, n := range g.Nodes {
		if n.World != world {
			continue
		}
		switch n.Kind {
		case graph.KindFunction, graph.KindClass, graph.KindInterface:
			table[n.Name] = append(table[n.Name], symbol{n.ID, n.Kind})
		case graph.KindMethod:
			name := n.Name
			if i := strings.LastIndexByte(name, '.'); i >= 0 {
				name = name[i+1:]
			}
			table[name] = append(table[name], symbol{n.ID, n.Kind})
		}
	}
	seen := map[string]bool{} // dedup edges: repeated calls to the same target collapse
	for _, ref := range pending {
		var match symbol
		count := 0
		for _, cand := range table[ref.target] {
			if !relAcceptsKind(ref.rel, cand.kind) {
				continue // wrong kind for this relation — never a valid target
			}
			match = cand
			count++
			if count > 1 {
				break // ambiguous; no need to keep scanning
			}
		}
		if count != 1 { // unresolved or ambiguous — skip (precision over recall)
			continue
		}
		if match.id == ref.srcID { // self-edge (recursion) — no navigational value
			continue
		}
		key := ref.rel + "\x00" + ref.srcID + "\x00" + match.id
		if seen[key] {
			continue
		}
		seen[key] = true
		g.Edges = append(g.Edges, graph.Edge{Src: ref.srcID, Dst: match.id, Rel: ref.rel})
	}
}

// relAcceptsKind gates which decl kinds a relation may resolve to, so a calls edge
// can never point at a class and an inherits edge can never point at a function.
// A precision guard layered on top of the exactly-one-definition rule.
func relAcceptsKind(rel, kind string) bool {
	switch rel {
	case graph.RelCalls:
		return kind == graph.KindFunction || kind == graph.KindMethod
	case graph.RelInherits:
		return kind == graph.KindClass
	case graph.RelImplements:
		return kind == graph.KindInterface
	}
	return false
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

// firstArgString returns the value of the FIRST positional argument iff it is a
// string literal. This distinguishes APIs where the topic/subject is positionally
// first (NATS nc.Publish("subj", ...)) from ones where a context leads
// (redis rdb.Publish(ctx, "channel", ...)) — the latter's first arg isn't a string.
func (p *fileCtx) firstArgString(argList *sitter.Node) (string, bool) {
	if argList == nil || argList.NamedChildCount() == 0 {
		return "", false
	}
	return p.stringLit(argList.NamedChild(0))
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

// skipFile filters out machine-generated sources: they bloat the structural graph
// with thousands of nodes nobody navigates, and the REAL producer/consumer calls
// (RegisterXServer / NewXClient) live in hand-written code, so cross-service edges
// survive the skip.
func skipFile(name string) bool {
	for _, suf := range []string{
		".pb.go", ".pb.gw.go", ".pb.validate.go", ".connect.go", ".gen.go", "_generated.go", // Go (protoc, grpc-gateway, protoc-gen-validate, connect-go)
		"_pb2.py", "_pb2_grpc.py", // Python protobuf
		".pb.ts", "_pb.ts", ".pb.js", "_pb.js", // TS/JS protobuf
	} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
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
