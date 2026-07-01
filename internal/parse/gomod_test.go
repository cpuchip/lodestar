package parse

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

func TestRequireModulePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"source.vivint.com/pl/grpc/v5 v5.2.1", "source.vivint.com/pl/grpc/v5"},
		{"golang.org/x/mod v0.1.0", "golang.org/x/mod"},
		{"source.vivint.com/pl/log/v2 v2.1.4 // a note", "source.vivint.com/pl/log/v2"}, // non-indirect comment kept
		{"github.com/foo/bar v1.0.0 // indirect", ""},                                   // transitive → skipped
		{"noversion", ""},                                                               // no v<version> → skip
		{"", ""},
	}
	for _, c := range cases {
		if got := requireModulePath(c.in); got != c.want {
			t.Errorf("requireModulePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGoModDetect(t *testing.T) {
	if !isGoModFile("go.mod") {
		t.Error("go.mod should be detected")
	}
	if isGoModFile("go.sum") || isGoModFile("main.go") {
		t.Error("misclassification")
	}
}

func TestParseGoMod(t *testing.T) {
	dir := t.TempDir()
	gomod := `module source.vivint.com/pl/example/v2

go 1.22

require (
	source.vivint.com/pl/grpc/v5 v5.2.1
	source.vivint.com/pl/log/v2 v2.1.4
	github.com/external/thing v1.0.0 // indirect
)

require source.vivint.com/pl/single v1.0.0
`
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &graph.Graph{Worlds: []string{"example"}}
	if err := parseGoMod(g, "example", "go.mod", path); err != nil {
		t.Fatal(err)
	}

	var publishes, deps []string
	for _, n := range g.Nodes {
		switch n.Kind {
		case graph.KindPackagePublish:
			publishes = append(publishes, n.Name)
		case graph.KindPackageDep:
			deps = append(deps, n.Name)
		}
	}
	sort.Strings(deps)
	if len(publishes) != 1 || publishes[0] != "source.vivint.com/pl/example/v2" {
		t.Errorf("publish = %v, want [source.vivint.com/pl/example/v2]", publishes)
	}
	want := []string{"source.vivint.com/pl/grpc/v5", "source.vivint.com/pl/log/v2", "source.vivint.com/pl/single"}
	if len(deps) != 3 || deps[0] != want[0] || deps[1] != want[1] || deps[2] != want[2] {
		t.Errorf("deps = %v, want %v (indirect external excluded)", deps, want)
	}
}
