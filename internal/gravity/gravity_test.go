package gravity

import (
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// build makes a graph with one node per world (ID == world) and an http cross-edge
// for each world pair listed, so Analyze can map edges → worlds.
func build(worlds []string, pairs [][2]string) *graph.Graph {
	g := &graph.Graph{Worlds: worlds}
	for _, w := range worlds {
		g.Nodes = append(g.Nodes, graph.Node{ID: w, World: w})
	}
	for _, p := range pairs {
		g.CrossEdges = append(g.CrossEdges, graph.CrossEdge{Src: p[0], Dst: p[1], Protocol: "http"})
	}
	return g
}

// The gravity oracle: a system with two tight clusters joined by one weak bridge
// is HEALTHY (high modularity, two galaxies, not a black hole); a fully-connected
// uniform system is a BLACK HOLE (modularity ~0, dense). The point is that the two
// get opposite verdicts — the diagnostic separates them.
func TestGravitySeparatesClusteredFromBlackHole(t *testing.T) {
	// two triangles {a,b,c} and {d,e,f}, one bridge c-d
	clustered := build(
		[]string{"a", "b", "c", "d", "e", "f"},
		[][2]string{{"a", "b"}, {"b", "c"}, {"a", "c"}, {"d", "e"}, {"e", "f"}, {"d", "f"}, {"c", "d"}},
	)
	healthy := Analyze(clustered)
	if healthy.BlackHole {
		t.Errorf("clustered system misread as a black hole (Q=%v)", healthy.Modularity)
	}
	if healthy.Modularity < 0.3 {
		t.Errorf("clustered modularity = %v, want > 0.3 (clear clusters)", healthy.Modularity)
	}
	if len(healthy.Galaxies) < 2 {
		t.Errorf("clustered should resolve into >= 2 galaxies, got %d: %v", len(healthy.Galaxies), healthy.Galaxies)
	}

	// K6: every world bound to every other, uniformly
	var allPairs [][2]string
	ws := []string{"a", "b", "c", "d", "e", "f"}
	for i := 0; i < len(ws); i++ {
		for j := i + 1; j < len(ws); j++ {
			allPairs = append(allPairs, [2]string{ws[i], ws[j]})
		}
	}
	uniform := build(ws, allPairs)
	hole := Analyze(uniform)
	if !hole.BlackHole {
		t.Errorf("uniform K6 not flagged as a black hole (Q=%v, density=%v)", hole.Modularity, hole.Density)
	}
	if hole.Modularity > 0.15 {
		t.Errorf("uniform modularity = %v, want low (~0, no structure)", hole.Modularity)
	}
	if hole.Density != 1.0 {
		t.Errorf("K6 density = %v, want 1.0", hole.Density)
	}

	// the two systems get opposite verdicts — the whole point of the diagnostic
	if healthy.BlackHole == hole.BlackHole {
		t.Fatal("diagnostic failed to separate a healthy galaxy from a black hole")
	}
}

// TestGravityMass checks the heaviest world surfaces first and mass sums weights.
func TestGravityMass(t *testing.T) {
	// hub-and-spoke: h bound to a,b,c; each spoke bound only to h
	g := build([]string{"h", "a", "b", "c"}, [][2]string{{"h", "a"}, {"h", "b"}, {"h", "c"}})
	r := Analyze(g)
	if len(r.Worlds) == 0 || r.Worlds[0].World != "h" {
		t.Fatalf("heaviest world should be the hub h, got %+v", r.Worlds)
	}
	if r.Worlds[0].Mass != 3.0 {
		t.Errorf("hub mass = %v, want 3.0 (three http ties)", r.Worlds[0].Mass)
	}
}
