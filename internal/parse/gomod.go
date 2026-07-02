package parse

// go.mod is structured data (not code), so — like Helm/OpenAPI — it takes a
// parallel handler off the tree-sitter path. It carries the LIBRARY-coupling
// dimension the runtime resolvers can't see:
//
//   - `module <path>`   → this repo PUBLISHES module <path>  (a KindPackagePublish producer)
//   - `require <path> v` → this repo DEPENDS ON module <path> (a KindPackageDep consumer)
//
// resolve pairs a require against the repo that publishes that exact module path →
// a directional `depends_on` (protocol `package`) cross-edge (consumer → publisher).
// A shared lib pulled into 80+ repos thus becomes a heavy gravity center — the
// compile-time backbone that the http/grpc/pubsub call-graph is blind to.
//
// The key is the module path verbatim (version stripped) — Go's require path and
// the publisher's module line are the same string, so it's an exact key-join, no
// normalization guesswork. Externals (github.com/…, golang.org/…) simply never match
// a publisher in the graph and drop out at resolve time.

import (
	"bufio"
	"os"
	"strings"

	"github.com/cpuchip/lodestar/internal/graph"
)

func isGoModFile(name string) bool { return name == "go.mod" }

// parseGoMod reads a go.mod and emits the publish producer + one dep consumer per
// require. Handles both the `require (` block and single-line `require x v` forms;
// skips `// indirect` transitive deps (we want the repo's OWN direct coupling).
func parseGoMod(g *graph.Graph, world, rel, absPath string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()

	fileID := world + "::" + rel
	g.Nodes = append(g.Nodes, graph.Node{ID: fileID, World: world, Kind: graph.KindFile, Name: rel})
	p := &fileCtx{world: world, rel: rel, fileID: fileID, g: g, seen: map[string]bool{fileID: true}}

	inRequireBlock := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "module "):
			if m := strings.TrimSpace(strings.TrimPrefix(line, "module ")); m != "" {
				p.addContract(graph.KindPackagePublish, m, map[string]string{"source": "gomod"})
			}
		case line == "require (":
			inRequireBlock = true
		case inRequireBlock && line == ")":
			inRequireBlock = false
		case inRequireBlock:
			if mod := requireModulePath(line); mod != "" {
				p.addContract(graph.KindPackageDep, mod, map[string]string{"source": "gomod"})
			}
		case strings.HasPrefix(line, "require "):
			if mod := requireModulePath(strings.TrimPrefix(line, "require ")); mod != "" {
				p.addContract(graph.KindPackageDep, mod, map[string]string{"source": "gomod"})
			}
		}
	}
	return sc.Err()
}

// requireModulePath pulls the module path from a require entry ("<path> <version>
// [// indirect]"), or "" for an indirect dep (transitive — not this repo's own
// coupling) or a malformed line.
func requireModulePath(entry string) string {
	entry = strings.TrimSpace(entry)
	if strings.Contains(entry, "// indirect") {
		return "" // transitive; not a direct dependency of this repo
	}
	if i := strings.Index(entry, "//"); i >= 0 {
		entry = strings.TrimSpace(entry[:i])
	}
	fields := strings.Fields(entry)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "v") {
		return "" // not "<path> v<version>" — skip
	}
	return fields[0]
}
