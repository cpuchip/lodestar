// Package gravity turns the cross-world edges into a force map: how strongly each
// pair of worlds is bound, which worlds cluster into galaxies, and whether the
// whole system has healthy structure or has collapsed into a black hole — a
// distributed monolith where everything is uniformly bound and no service can be
// pulled out. The signal is modularity: clear clusters with sparse links score
// high; a uniformly-connected blob scores near zero.
package gravity

import (
	"sort"

	"github.com/cpuchip/lodestar/internal/graph"
)

// protocolWeight is how heavily each kind of tie binds two worlds. A shared
// database is a much stronger coupling than one HTTP call; pub-sub is looser
// (decoupled by design). Weights are the gravity constants.
var protocolWeight = map[string]float64{
	"http": 1.0, "grpc": 1.0, "pubsub": 0.5,
	"schema": 2.0, "db": 3.0, "config": 1.5, "package": 1.5,
	// k8s: a declared deploy-time service dependency (Helm values *_HOST/_ADDR ref)
	// — a direct runtime bind, heavier than an ad-hoc call, on par with a shared
	// schema. (Tunable — Michael, 2026-07-01: "if it's a direct service dep the
	// gravity should be higher, 1.5 maybe even 2 — we should play with that.")
	"k8s": 2.0,
}

func weightOf(protocol string) float64 {
	if w, ok := protocolWeight[protocol]; ok {
		return w
	}
	return 1.0
}

// WorldMass is a world's total gravitational pull (sum of incident edge weights).
type WorldMass struct {
	World string  `json:"world"`
	Mass  float64 `json:"mass"`
}

// Report is the gravity analysis of a graph.
type Report struct {
	Worlds      []WorldMass `json:"worlds"`       // by mass, heaviest first
	Galaxies    [][]string  `json:"galaxies"`     // communities (each sorted)
	Modularity  float64     `json:"modularity"`   // Q of the partition; ~0 = no structure
	PairEdges   int         `json:"pair_edges"`   // distinct world-pairs bound
	Density    float64 `json:"density"`     // bound pairs / possible pairs
	BlackHole  bool    `json:"black_hole"`  // low modularity + dense + several worlds
}

// Analyze computes the gravity report for a graph's cross-world edges.
func Analyze(g *graph.Graph) Report {
	// Aggregate undirected weighted world adjacency.
	adj := map[string]map[string]float64{}
	addWeight := func(a, b string, w float64) {
		if a == b {
			return
		}
		if adj[a] == nil {
			adj[a] = map[string]float64{}
		}
		adj[a][b] += w
	}
	for _, e := range g.CrossEdges {
		wa := worldOf(g, e.Src)
		wb := worldOf(g, e.Dst)
		if wa == "" || wb == "" || wa == wb {
			continue
		}
		w := weightOf(e.Protocol)
		addWeight(wa, wb, w)
		addWeight(wb, wa, w)
	}

	worlds := sortedWorlds(g, adj)

	// World mass = weighted degree.
	deg := map[string]float64{}
	var twoM float64
	for a, nbrs := range adj {
		for _, w := range nbrs {
			deg[a] += w
			twoM += w // each undirected edge counted from both ends → this is 2m
		}
	}

	masses := make([]WorldMass, 0, len(worlds))
	for _, w := range worlds {
		masses = append(masses, WorldMass{World: w, Mass: deg[w]})
	}
	sort.SliceStable(masses, func(i, j int) bool {
		if masses[i].Mass != masses[j].Mass {
			return masses[i].Mass > masses[j].Mass
		}
		return masses[i].World < masses[j].World
	})

	comm := detectCommunities(worlds, adj, deg, twoM)
	q := modularity(adj, deg, twoM, comm)
	galaxies := galaxiesOf(comm)

	pairEdges := 0
	for a, nbrs := range adj {
		for b := range nbrs {
			if a < b {
				pairEdges++
			}
		}
	}
	n := len(worlds)
	possible := n * (n - 1) / 2
	density := 0.0
	if possible > 0 {
		density = float64(pairEdges) / float64(possible)
	}

	// A black hole: several worlds, densely bound, yet no community structure —
	// everything pulls on everything, nothing separates.
	blackHole := n >= 4 && density >= 0.5 && q < 0.2

	return Report{
		Worlds:     masses,
		Galaxies:   galaxies,
		Modularity: round4(q),
		PairEdges:  pairEdges,
		Density:    round4(density),
		BlackHole:  blackHole,
	}
}

// detectCommunities runs Louvain local-moving: each world starts alone, then is
// moved to the neighbor community that most increases modularity — and only if
// that beats staying alone. Unlike raw label propagation, the modularity-gain test
// won't avalanche two tight clusters into one across a single weak bridge.
// Deterministic: worlds in sorted order, ties → lowest community label.
func detectCommunities(worlds []string, adj map[string]map[string]float64, deg map[string]float64, twoM float64) map[string]string {
	comm := map[string]string{}
	for _, w := range worlds {
		comm[w] = w
	}
	if twoM == 0 {
		return comm
	}
	sigmaTot := map[string]float64{} // total degree currently in each community
	for _, w := range worlds {
		sigmaTot[w] = deg[w]
	}
	for iter := 0; iter < 100; iter++ {
		changed := false
		for _, i := range worlds {
			ci := comm[i]
			sigmaTot[ci] -= deg[i] // tentatively remove i from its community

			kIn := map[string]float64{} // weight from i into each community
			for nbr, w := range adj[i] {
				kIn[comm[nbr]] += w
			}
			// gain(C) = k_i_in(C) - sigmaTot(C)*deg_i/2m; staying alone = 0.
			best, bestGain := i, 0.0
			cands := []string{ci}
			for c := range kIn {
				cands = append(cands, c)
			}
			sort.Strings(cands)
			for _, c := range cands {
				gain := kIn[c] - sigmaTot[c]*deg[i]/twoM
				if gain > bestGain || (gain == bestGain && c < best) {
					best, bestGain = c, gain
				}
			}
			sigmaTot[best] += deg[i]
			if best != ci {
				comm[i] = best
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return comm
}

// modularity is weighted Newman modularity Q for the given partition.
// Q = Σ_c [ L_c/m - (D_c/2m)^2 ], with m = total edge weight = twoM/2.
func modularity(adj map[string]map[string]float64, deg map[string]float64, twoM float64, comm map[string]string) float64 {
	if twoM == 0 {
		return 0
	}
	m := twoM / 2
	lIn := map[string]float64{}  // internal weight per community (undirected, once)
	dTot := map[string]float64{} // total degree per community
	for a, nbrs := range adj {
		dTot[comm[a]] += deg[a]
		for b, w := range nbrs {
			if comm[a] == comm[b] && a < b {
				lIn[comm[a]] += w
			}
		}
	}
	var q float64
	for _, l := range lIn {
		q += l / m
	}
	for _, d := range dTot {
		frac := d / twoM
		q -= frac * frac
	}
	return q
}

func galaxiesOf(comm map[string]string) [][]string {
	groups := map[string][]string{}
	for w, c := range comm {
		groups[c] = append(groups[c], w)
	}
	out := make([][]string, 0, len(groups))
	for _, members := range groups {
		sort.Strings(members)
		out = append(out, members)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i][0] < out[j][0]
	})
	return out
}

// worldOf returns the world a node ID belongs to, via the node's declared world.
func worldOf(g *graph.Graph, nodeID string) string {
	for _, n := range g.Nodes {
		if n.ID == nodeID {
			return n.World
		}
	}
	return ""
}

// sortedWorlds is the union of graph worlds and any that appear in the adjacency.
func sortedWorlds(g *graph.Graph, adj map[string]map[string]float64) []string {
	set := map[string]bool{}
	for _, w := range g.Worlds {
		set[w] = true
	}
	for a, nbrs := range adj {
		set[a] = true
		for b := range nbrs {
			set[b] = true
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

func round4(f float64) float64 {
	return float64(int(f*10000+0.5)) / 10000
}
