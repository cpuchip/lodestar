// Command lodestar maps one or more repos into a cross-service code graph.
//
// Usage:
//
//	lodestar <repo-path-or-url> [more...]   # → graph JSON on stdout
//
// This is the entry point; the parse / contract / resolve / emit pipeline is
// being built behind it (see docs/ARCHITECTURE.md). For now it prints usage.
package main

import (
	"fmt"
	"os"
)

const tagline = "lodestar — navigate any codebase by its gravity"

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Fprintln(os.Stderr, tagline)
		fmt.Fprintln(os.Stderr, "usage: lodestar <repo-path-or-url> [more...]")
		fmt.Fprintln(os.Stderr, "       emits a cross-service code graph (JSON) for the given repos")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, tagline)
	fmt.Fprintln(os.Stderr, "not yet implemented — the parse/contract/resolve pipeline is in build.")
	fmt.Fprintln(os.Stderr, "see docs/ARCHITECTURE.md")
	os.Exit(1)
}
