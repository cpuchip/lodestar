package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// The parser is deterministic, so its test is an oracle: recall (every real
// declaration shows up with the right kind), precision (composite literals and
// call expressions do NOT masquerade as declarations), structure (every decl is
// contained by its file), and discrimination (a method is receiver-qualified, so
// two Start methods on different types don't collide).
const sampleGo = `package sample

import (
	"fmt"
	"net/http"
)

type Server struct {
	addr string
}

type Handler interface {
	Serve()
}

func New(addr string) *Server {
	return &Server{addr: addr}
}

func (s *Server) Start() error {
	fmt.Println(s.addr)
	return http.ErrServerClosed
}

func (h handlerImpl) Start() {}

type handlerImpl struct{}
`

func parseSample(t *testing.T) *graph.Graph {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.go"), []byte(sampleGo), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	return g
}

func TestParseGoSkeleton(t *testing.T) {
	g := parseSample(t)

	byKindName := map[string]graph.Node{}
	for _, n := range g.Nodes {
		byKindName[n.Kind+" "+n.Name] = n
	}

	// recall — every declaration, with the right kind
	want := []struct{ kind, name string }{
		{graph.KindFile, "server.go"},
		{graph.KindClass, "Server"},
		{graph.KindInterface, "Handler"},
		{graph.KindClass, "handlerImpl"},
		{graph.KindFunction, "New"},
		{graph.KindMethod, "Server.Start"},
		{graph.KindMethod, "handlerImpl.Start"},
	}
	for _, w := range want {
		if _, ok := byKindName[w.kind+" "+w.name]; !ok {
			t.Errorf("recall: missing %s %q", w.kind, w.name)
		}
	}

	// precision — exactly these nodes, nothing spurious (no composite-literal
	// "Server" node, no node for the fmt.Println / http.ErrServerClosed refs)
	if len(g.Nodes) != len(want) {
		var got []string
		for _, n := range g.Nodes {
			got = append(got, n.Kind+" "+n.Name)
		}
		t.Errorf("precision: got %d nodes, want %d: %v", len(g.Nodes), len(want), got)
	}

	// discrimination — the two Start methods are distinct, receiver-qualified
	if m := byKindName[graph.KindMethod+" Server.Start"]; m.Metadata["receiver"] != "Server" {
		t.Errorf("discrimination: Server.Start receiver = %q, want Server", m.Metadata["receiver"])
	}
	if _, ok := byKindName[graph.KindMethod+" handlerImpl.Start"]; !ok {
		t.Error("discrimination: handlerImpl.Start collapsed into Server.Start")
	}

	// structure — every non-file node is contained by the file
	fileID := "svc::server.go"
	contained := map[string]bool{}
	for _, e := range g.Edges {
		if e.Rel == graph.RelContains && e.Src == fileID {
			contained[e.Dst] = true
		}
	}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindFile {
			continue
		}
		if !contained[n.ID] {
			t.Errorf("structure: %s %q not contained by its file", n.Kind, n.Name)
		}
	}

	// imports captured on the file node as metadata (not as fake nodes)
	file := byKindName[graph.KindFile+" server.go"]
	if file.Metadata["imports"] != "fmt net/http" {
		t.Errorf("imports: file metadata = %q, want %q", file.Metadata["imports"], "fmt net/http")
	}
}

// TestParseDirSkipsVendored proves WalkDir prunes dependency dirs so the graph
// only carries code the service owns.
func TestParseDirSkipsVendored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package m\nfunc Real(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vend := filepath.Join(dir, "vendor", "dep")
	if err := os.MkdirAll(vend, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vend, "dep.go"), []byte("package dep\nfunc Vendored(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.Name == "Vendored" {
			t.Fatal("vendored code leaked into the graph")
		}
	}
}
