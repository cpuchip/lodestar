package split

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func nodeIDs(g *graph.Graph) map[string]graph.Node {
	m := map[string]graph.Node{}
	for _, n := range g.Nodes {
		m[n.ID] = n
	}
	return m
}

// A monorepo with two charted services + one un-charted tooling binary + shared
// code: only the two charted services split; the tester and shared code stay in
// the repo world; every node ID and edge endpoint is remapped consistently.
func TestSplitWorlds_ChartsGate(t *testing.T) {
	repo := t.TempDir()
	mkdirs(t, repo, "cmd/svc-a", "cmd/svc-b", "cmd/tester", "charts/svc-a", "charts/svc-b", "charts/common", "internal")
	W := "mono"
	g := &graph.Graph{
		Worlds: []string{W},
		Nodes: []graph.Node{
			{ID: W + "::cmd/svc-a/main.go", World: W, Kind: graph.KindFile, Name: "cmd/svc-a/main.go"},
			{ID: W + "::cmd/svc-a/main.go::grpc_service::SvcA", World: W, Kind: graph.KindGRPCService, Name: "SvcA"},
			{ID: W + "::cmd/svc-b/main.go", World: W, Kind: graph.KindFile, Name: "cmd/svc-b/main.go"},
			{ID: W + "::cmd/svc-b/main.go::grpc_client::SvcA", World: W, Kind: graph.KindGRPCClient, Name: "SvcA"},
			{ID: W + "::cmd/tester/main.go", World: W, Kind: graph.KindFile, Name: "cmd/tester/main.go"},
			{ID: W + "::internal/shared.go", World: W, Kind: graph.KindFile, Name: "internal/shared.go"},
		},
		Edges: []graph.Edge{
			{Src: W + "::cmd/svc-a/main.go", Dst: W + "::internal/shared.go", Rel: graph.RelImports},
		},
	}

	if got := SplitWorlds(g, W, repo, DefaultOptions()); got != 2 {
		t.Fatalf("sub-worlds: want 2 (svc-a, svc-b), got %d — worlds=%v", got, g.Worlds)
	}
	by := nodeIDs(g)

	// charted services moved to their own world (ID prefix swapped, World updated)
	if n, ok := by["svc-a::cmd/svc-a/main.go::grpc_service::SvcA"]; !ok || n.World != "svc-a" {
		t.Errorf("svc-a grpc_service not remapped: %+v (ok=%v)", n, ok)
	}
	if n, ok := by["svc-b::cmd/svc-b/main.go::grpc_client::SvcA"]; !ok || n.World != "svc-b" {
		t.Errorf("svc-b grpc_client not remapped: %+v (ok=%v)", n, ok)
	}
	// un-charted tooling binary stays in the repo world (gated out)
	if n, ok := by["mono::cmd/tester/main.go"]; !ok || n.World != "mono" {
		t.Errorf("tester should stay in mono (no chart): %+v (ok=%v)", n, ok)
	}
	// shared code stays in the repo world
	if _, ok := by["mono::internal/shared.go"]; !ok {
		t.Errorf("shared code should stay in mono")
	}
	// the structural edge re-points across worlds (svc-a -> mono shared)
	if g.Edges[0].Src != "svc-a::cmd/svc-a/main.go" || g.Edges[0].Dst != "mono::internal/shared.go" {
		t.Errorf("edge endpoints not remapped: %+v", g.Edges[0])
	}
	if len(g.Worlds) != 3 {
		t.Errorf("worlds list: want [mono svc-a svc-b], got %v", g.Worlds)
	}
}

// No gate dir → every non-generic candidate under the root globs splits.
func TestSplitWorlds_NoGateSplitsAll(t *testing.T) {
	repo := t.TempDir()
	mkdirs(t, repo, "cmd/svc-a", "cmd/worker", "cmd/server") // no charts/; "server" is generic → held
	W := "svc"
	g := &graph.Graph{Worlds: []string{W}, Nodes: []graph.Node{
		{ID: W + "::cmd/svc-a/m.go", World: W, Kind: graph.KindFile, Name: "cmd/svc-a/m.go"},
		{ID: W + "::cmd/worker/m.go", World: W, Kind: graph.KindFile, Name: "cmd/worker/m.go"},
		{ID: W + "::cmd/server/m.go", World: W, Kind: graph.KindFile, Name: "cmd/server/m.go"},
	}}
	if got := SplitWorlds(g, W, repo, DefaultOptions()); got != 2 {
		t.Fatalf("want 2 (svc-a, worker; server is generic), got %d — worlds=%v", got, g.Worlds)
	}
	by := nodeIDs(g)
	if _, ok := by["svc::cmd/server/m.go"]; !ok {
		t.Errorf("generic 'server' should stay in the repo world")
	}
}

// A single-service repo (no root markers) is untouched — a strict no-op.
func TestSplitWorlds_NoRootsNoOp(t *testing.T) {
	repo := t.TempDir()
	W := "flat"
	g := &graph.Graph{Worlds: []string{W}, Nodes: []graph.Node{
		{ID: W + "::main.go", World: W, Kind: graph.KindFile, Name: "main.go"},
	}}
	if got := SplitWorlds(g, W, repo, DefaultOptions()); got != 0 {
		t.Fatalf("no-op expected, got %d", got)
	}
	if g.Nodes[0].ID != "flat::main.go" || g.Nodes[0].World != "flat" {
		t.Errorf("node should be untouched: %+v", g.Nodes[0])
	}
}
