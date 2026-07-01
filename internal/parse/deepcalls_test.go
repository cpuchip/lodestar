package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// The deep-call-graph pass is deterministic, so its test is an oracle across three
// axes:
//   - RECALL: a bare call and a receiver-method call resolve to the right node; a
//     class extending a base / implementing an interface get inherits/implements.
//   - PRECISION: a stdlib / undefined callee produces NO edge; an ambiguous name
//     (two defs in the world) produces NO edge; edges never cross a world boundary.
//   - RESOLUTION: cross-FILE targets resolve (a call in file A to a func in file B),
//     since Pass 2 runs after every file of the world is parsed.

// edgeSet indexes edges of a relation as "srcName->dstName" using the node table so
// tests can assert on human-facing names rather than opaque IDs.
func edgeSet(g *graph.Graph, rel string) map[string]bool {
	name := map[string]string{}
	for _, n := range g.Nodes {
		name[n.ID] = n.Name
	}
	out := map[string]bool{}
	for _, e := range g.Edges {
		if e.Rel == rel {
			out[name[e.Src]+"->"+name[e.Dst]] = true
		}
	}
	return out
}

func parseFiles(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	return g
}

// --- Go: calls, cross-file resolution, precision ---

func TestDeepCallsGo(t *testing.T) {
	// helper() lives in a DIFFERENT file than its caller — proves Pass-2 cross-file
	// resolution. setup() is a method on the same receiver as its caller.
	server := `package svc

func New(addr string) *Server {
	s := &Server{addr: addr}
	helper()
	return s
}

type Server struct{ addr string }

func (s *Server) Start() error {
	s.setup()
	fmt.Println(s.addr)   // stdlib — NO edge
	len(s.addr)           // builtin — NO edge
	return nil
}

func (s *Server) setup() {}
`
	helpers := `package svc

func helper() { format() }

func format() {}
`
	g := parseFiles(t, map[string]string{"server.go": server, "helpers.go": helpers})
	calls := edgeSet(g, graph.RelCalls)

	// recall — bare call resolves cross-file; method call resolves on bare name
	want := []string{
		"New->helper",        // New() calls helper(), defined in helpers.go
		"Server.Start->Server.setup", // s.setup() resolves to the unique setup method
		"helper->format",     // chained resolution, same file
	}
	for _, w := range want {
		if !calls[w] {
			t.Errorf("recall: missing calls edge %q (got %v)", w, keys(calls))
		}
	}

	// precision — stdlib/builtin callees produce no edge (no world symbol)
	for bad := range calls {
		if bad == "Server.Start->Println" || bad == "Server.Start->len" {
			t.Errorf("precision: stdlib/builtin call leaked as edge %q", bad)
		}
	}
	// precision — every calls edge points at a node that exists in THIS world
	assertEdgesInWorld(t, g, graph.RelCalls)
}

// TestDeepCallsGoAmbiguous proves an ambiguous target (two defs sharing a name)
// yields NO edge — precision over recall, we cannot know which one was meant.
func TestDeepCallsGoAmbiguous(t *testing.T) {
	src := `package svc

func caller() { doIt() }

func (a A) doIt() {}
func (b B) doIt() {}

type A struct{}
type B struct{}
`
	g := parseFiles(t, map[string]string{"amb.go": src})
	calls := edgeSet(g, graph.RelCalls)
	for e := range calls {
		if e == "caller->A.doIt" || e == "caller->B.doIt" {
			t.Errorf("precision: ambiguous target must not resolve, got %q", e)
		}
	}
}

// TestDeepCallsGoNoInherits documents that Go emits no inherits/implements edges
// (struct/interface embedding is a different relation, deliberately skipped).
func TestDeepCallsGoNoInherits(t *testing.T) {
	src := `package svc

type Base struct{}
type Derived struct{ Base }   // struct embedding — NOT inherits

type Reader interface{ Read() }
type ReadWriter interface{ Reader }  // interface embedding — NOT inherits
`
	g := parseFiles(t, map[string]string{"embed.go": src})
	if inh := edgeSet(g, graph.RelInherits); len(inh) != 0 {
		t.Errorf("Go should emit no inherits edges, got %v", keys(inh))
	}
	if impl := edgeSet(g, graph.RelImplements); len(impl) != 0 {
		t.Errorf("Go should emit no implements edges, got %v", keys(impl))
	}
}

// --- Python: calls + inherits ---

func TestDeepCallsPython(t *testing.T) {
	base := `class Base:
    def boot(self):
        pass
`
	server := `class Server(Base):
    def start(self):
        self.setup()
        helper()
        print("x")      # builtin — NO edge

    def setup(self):
        pass

def helper():
    pass
`
	g := parseFiles(t, map[string]string{"base.py": base, "server.py": server})

	calls := edgeSet(g, graph.RelCalls)
	for _, w := range []string{"Server.start->Server.setup", "Server.start->helper"} {
		if !calls[w] {
			t.Errorf("recall: missing calls edge %q (got %v)", w, keys(calls))
		}
	}
	if calls["Server.start->print"] {
		t.Error("precision: builtin print() must not resolve")
	}

	// inherits — Server(Base) resolves cross-file to the Base class
	inh := edgeSet(g, graph.RelInherits)
	if !inh["Server->Base"] {
		t.Errorf("recall: missing inherits Server->Base (got %v)", keys(inh))
	}
	assertEdgesInWorld(t, g, graph.RelCalls)
	assertEdgesInWorld(t, g, graph.RelInherits)
}

// --- TS: calls + inherits + implements ---

func TestDeepCallsTS(t *testing.T) {
	src := `interface Widget {
  id: number;
}

class Base {
  boot(): void {}
}

class Server extends Base implements Widget {
  id = 1;
  start(): void {
    this.setup();
    helper();
    console.log("x");   // stdlib — NO edge
  }
  setup(): void {}
}

function helper(): void {}
`
	g := parseFiles(t, map[string]string{"app.ts": src})

	calls := edgeSet(g, graph.RelCalls)
	for _, w := range []string{"Server.start->Server.setup", "Server.start->helper"} {
		if !calls[w] {
			t.Errorf("recall: missing calls edge %q (got %v)", w, keys(calls))
		}
	}
	if calls["Server.start->log"] {
		t.Error("precision: console.log must not resolve")
	}

	inh := edgeSet(g, graph.RelInherits)
	if !inh["Server->Base"] {
		t.Errorf("recall: missing inherits Server->Base (got %v)", keys(inh))
	}
	impl := edgeSet(g, graph.RelImplements)
	if !impl["Server->Widget"] {
		t.Errorf("recall: missing implements Server->Widget (got %v)", keys(impl))
	}
	// precision — inherits target is a class, implements target is an interface;
	// they must not swap (relAcceptsKind guards this).
	if impl["Server->Base"] {
		t.Error("precision: Base is a class, must not be an implements target")
	}
	if inh["Server->Widget"] {
		t.Error("precision: Widget is an interface, must not be an inherits target")
	}
	assertEdgesInWorld(t, g, graph.RelCalls)
}

// TestDeepCallsJS proves the shared TS extractor records calls + extends for the
// javascript grammar too (JS has no implements clause).
func TestDeepCallsJS(t *testing.T) {
	src := `class Base {}
class Client extends Base {
  send() { this.flush(); run(); }
  flush() {}
}
function run() {}
`
	g := parseFiles(t, map[string]string{"app.js": src})
	calls := edgeSet(g, graph.RelCalls)
	for _, w := range []string{"Client.send->Client.flush", "Client.send->run"} {
		if !calls[w] {
			t.Errorf(".js recall: missing calls edge %q (got %v)", w, keys(calls))
		}
	}
	if inh := edgeSet(g, graph.RelInherits); !inh["Client->Base"] {
		t.Errorf(".js recall: missing inherits Client->Base (got %v)", keys(inh))
	}
}

// assertEdgesInWorld is the cross-world guard: every edge of a relation must point
// at nodes that exist in the world (deep resolution is intra-world only).
func assertEdgesInWorld(t *testing.T, g *graph.Graph, rel string) {
	t.Helper()
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if e.Rel != rel {
			continue
		}
		if !ids[e.Src] || !ids[e.Dst] {
			t.Errorf("world boundary: %s edge %s->%s references a node outside the world", rel, e.Src, e.Dst)
		}
	}
}
