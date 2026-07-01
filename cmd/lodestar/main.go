// Command lodestar maps one or more repos into a cross-service code graph.
//
// Usage:
//
//	lodestar [-world name] <repo-path> [more-repos...]
//
// Each repo becomes a "world"; lodestar parses its structural skeleton (files,
// functions, methods, types) and emits the combined graph as JSON on stdout.
// The contract layer (HTTP/gRPC/pub-sub resolvers that link worlds) is being
// built behind this — see docs/ARCHITECTURE.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cpuchip/lodestar/internal/graph"
	"github.com/cpuchip/lodestar/internal/parse"
	"github.com/cpuchip/lodestar/internal/resolve"
)

const tagline = "lodestar — navigate any codebase by its gravity"

func main() {
	world := flag.String("world", "", "world (service) name for a single repo; defaults to the directory's base name")
	flag.Usage = usage
	flag.Parse()
	repos := flag.Args()
	if len(repos) == 0 {
		usage()
		os.Exit(2)
	}
	if *world != "" && len(repos) > 1 {
		fmt.Fprintln(os.Stderr, "lodestar: -world applies to a single repo; with multiple repos each is named by its directory")
		os.Exit(2)
	}

	combined := &graph.Graph{}
	for _, repo := range repos {
		name := *world
		if name == "" {
			name = worldName(repo)
		}
		g, err := parse.ParseDir(name, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lodestar: parsing %s: %v\n", repo, err)
			os.Exit(1)
		}
		merge(combined, g)
	}

	// Pair producers and consumers across worlds into cross-service edges.
	resolve.Resolve(combined)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(combined); err != nil {
		fmt.Fprintf(os.Stderr, "lodestar: encoding graph: %v\n", err)
		os.Exit(1)
	}
}

// worldName derives a service name from a repo path: its cleaned base name.
func worldName(repo string) string {
	base := filepath.Base(filepath.Clean(repo))
	if base == "." || base == string(filepath.Separator) || base == "" {
		if abs, err := filepath.Abs(repo); err == nil {
			base = filepath.Base(abs)
		}
	}
	return base
}

// merge folds src into dst (worlds/nodes/edges/cross-edges concatenated).
func merge(dst, src *graph.Graph) {
	dst.Worlds = append(dst.Worlds, src.Worlds...)
	dst.Nodes = append(dst.Nodes, src.Nodes...)
	dst.Edges = append(dst.Edges, src.Edges...)
	dst.CrossEdges = append(dst.CrossEdges, src.CrossEdges...)
}

func usage() {
	fmt.Fprintln(os.Stderr, tagline)
	fmt.Fprintln(os.Stderr, "usage: lodestar [-world name] <repo-path> [more-repos...]")
	fmt.Fprintln(os.Stderr, "       parses each repo's structural skeleton and emits the combined graph (JSON) on stdout")
	fmt.Fprintln(os.Stderr, "languages: "+strings.Join(languageNames(), ", "))
}

func languageNames() []string {
	var out []string
	for _, l := range parse.Languages() {
		out = append(out, l.Name)
	}
	return out
}
