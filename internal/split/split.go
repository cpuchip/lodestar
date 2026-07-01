// Package split turns a multi-service monorepo into multiple worlds.
//
// lodestar's default is world == repo. That is wrong for a monorepo that ships
// several deployable services from one git repo (e.g. cmd/<svc>/ binaries, each
// with its own Helm chart): collapsing them into one world hides the intra-repo
// service topology and mislabels the map. SplitWorlds re-derives a per-service
// world from each node's path AFTER structural parsing but BEFORE cross-service
// resolution, so intra-repo service-to-service calls become real cross-edges.
//
// Generic mechanism, house profile: the split is driven by configurable markers.
// RootGlobs name the dirs whose immediate children are candidate services
// (default cmd/*). GateGlobs, when a matching dir exists in the repo, restrict the
// split to candidates whose basename also appears under a gate dir (default
// charts/*) — i.e. "only split cmd/ services that are actually deployed as their
// own chart." A repo with no gate dir splits every candidate. Nothing here is
// Vivint-specific: cmd/* + charts/* are conventional; a downstream profile can
// pass any markers (apps/*, services/*, deploy/*, …).
package split

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cpuchip/lodestar/internal/graph"
)

// Options configures service-root world splitting.
type Options struct {
	RootGlobs []string        // dir globs whose immediate children are candidate services
	GateGlobs []string        // when a match exists, only candidates whose basename appears here are split
	Generic   map[string]bool // candidate names too generic to be a distinct service (kept in the repo world)
}

// DefaultOptions is the conventional profile: cmd/* services, gated by charts/*,
// with common non-service binary names held back.
func DefaultOptions() Options {
	return Options{
		RootGlobs: []string{"cmd/*"},
		GateGlobs: []string{"charts/*"},
		Generic: map[string]bool{
			"server": true, "main": true, "app": true, "cmd": true, "cli": true,
			"test": true, "tester": true, "tests": true, "tool": true, "tools": true,
			"e2e": true, "integration": true, "migrate": true, "migration": true,
		},
	}
}

// SplitWorlds rewrites g — a single-world graph for repoWorld rooted at repoDir —
// so files under a detected service root become their own world; everything else
// (shared internal/, pkg/, root) stays in repoWorld. Node IDs (which embed the
// world as the first "::"-segment) and every edge / cross-edge reference are
// remapped consistently. Returns the number of sub-worlds created (0 = no-op).
// Idempotent for a graph with no service roots.
func SplitWorlds(g *graph.Graph, repoWorld, repoDir string, opts Options) int {
	roots := detectServiceRoots(repoDir, opts) // relPrefix (e.g. "cmd/foo/") -> sub-world name
	if len(roots) == 0 {
		return 0
	}

	remap := make(map[string]string, len(g.Nodes)) // old node ID -> new node ID
	worldSet := map[string]bool{repoWorld: true}

	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.World != repoWorld {
			continue // already split / foreign — leave it
		}
		sub := subWorldFor(relOf(n.ID, repoWorld), roots)
		if sub == "" || sub == repoWorld {
			continue
		}
		newID := sub + n.ID[len(repoWorld):] // ID starts with repoWorld+"::"; swap the prefix
		remap[n.ID] = newID
		n.ID = newID
		n.World = sub
		worldSet[sub] = true
	}
	if len(remap) == 0 {
		return 0
	}

	// Re-point every edge/cross-edge endpoint that moved. An edge whose two ends
	// land in different worlds (e.g. cmd/<svc> code calling shared internal/) stays
	// an intra-graph structural edge with src.World != dst.World — the consumer
	// keys navigation off the node worlds, and it correctly shows shared-code gravity.
	for i := range g.Edges {
		if nid, ok := remap[g.Edges[i].Src]; ok {
			g.Edges[i].Src = nid
		}
		if nid, ok := remap[g.Edges[i].Dst]; ok {
			g.Edges[i].Dst = nid
		}
	}
	for i := range g.CrossEdges {
		if nid, ok := remap[g.CrossEdges[i].Src]; ok {
			g.CrossEdges[i].Src = nid
		}
		if nid, ok := remap[g.CrossEdges[i].Dst]; ok {
			g.CrossEdges[i].Dst = nid
		}
	}

	g.Worlds = make([]string, 0, len(worldSet))
	for w := range worldSet {
		g.Worlds = append(g.Worlds, w)
	}
	sort.Strings(g.Worlds)
	return len(worldSet) - 1
}

// detectServiceRoots returns repo-relative prefixes ("cmd/foo/") mapped to the
// sub-world name they define. Honors the gate: if any GateGlob dir has children,
// only candidates whose basename appears among the gate children are kept.
func detectServiceRoots(repoDir string, opts Options) map[string]string {
	gate, gated := gateNames(repoDir, opts.GateGlobs)
	roots := map[string]string{}
	for _, glob := range opts.RootGlobs {
		matches, _ := filepath.Glob(filepath.Join(repoDir, filepath.FromSlash(glob)))
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || !fi.IsDir() {
				continue
			}
			name := filepath.Base(m)
			if opts.Generic[strings.ToLower(name)] {
				continue
			}
			if gated && !gate[name] {
				continue // a gate exists and this candidate isn't in it — not a deployed service
			}
			rel, err := filepath.Rel(repoDir, m)
			if err != nil {
				continue
			}
			roots[filepath.ToSlash(rel)+"/"] = name
		}
	}
	return roots
}

// gateNames collects the basenames under the gate globs. gated is false when no
// gate dir exists (so the caller splits every candidate).
func gateNames(repoDir string, globs []string) (map[string]bool, bool) {
	names := map[string]bool{}
	for _, glob := range globs {
		matches, _ := filepath.Glob(filepath.Join(repoDir, filepath.FromSlash(glob)))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				names[filepath.Base(m)] = true
			}
		}
	}
	return names, len(names) > 0
}

// relOf extracts the repo-relative path (the segment between the 1st and 2nd
// "::") from a node ID of the form world::rel[::kind[::key]] or world::rel.
func relOf(id, world string) string {
	rest := strings.TrimPrefix(id, world+"::")
	if i := strings.Index(rest, "::"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// subWorldFor returns the sub-world whose service-root prefix the rel path is
// under, or "" for shared/root code.
func subWorldFor(rel string, roots map[string]string) string {
	for prefix, name := range roots {
		if strings.HasPrefix(rel, prefix) {
			return name
		}
	}
	return ""
}
