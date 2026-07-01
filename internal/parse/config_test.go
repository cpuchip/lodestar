package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// configVars parses one file of a language and returns the set of env var names
// emitted as KindConfigKey nodes.
func configVars(t *testing.T, filename, src string) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindConfigKey {
			got[n.Name] = true
		}
	}
	return got
}

// The config oracle: each language surfaces static env-var reads as KindConfigKey
// nodes keyed by the var name as-written; a dynamic name is skipped (precision).
func TestExtractGoConfig(t *testing.T) {
	got := configVars(t, "cfg.go", `package svc
func f() {
	_ = os.Getenv("DATABASE_URL")
	v, _ := os.LookupEnv("REDIS_ADDR")
	_ = os.Getenv(dynamicName)   // dynamic → skipped
	_ = other.Getenv("NOT_OS")   // not the os package → skipped
	_ = v
}`)
	for _, want := range []string{"DATABASE_URL", "REDIS_ADDR"} {
		if !got[want] {
			t.Errorf("recall: missing Go env var %q (got %v)", want, keys(got))
		}
	}
	if got["NOT_OS"] {
		t.Error("precision: non-os Getenv must not be read as an env var")
	}
	if len(got) != 2 {
		t.Errorf("precision: want exactly 2 vars, got %v", keys(got))
	}
}

func TestExtractPythonConfig(t *testing.T) {
	got := configVars(t, "cfg.py", `import os
def f():
    a = os.environ["DATABASE_URL"]
    b = os.environ.get("REDIS_ADDR")
    c = os.getenv("KAFKA_BROKER")
    d = os.environ.get(dyn)      # dynamic → skipped
`)
	for _, want := range []string{"DATABASE_URL", "REDIS_ADDR", "KAFKA_BROKER"} {
		if !got[want] {
			t.Errorf("recall: missing Python env var %q (got %v)", want, keys(got))
		}
	}
	if len(got) != 3 {
		t.Errorf("precision: want exactly 3 vars, got %v", keys(got))
	}
}

func TestExtractTSConfig(t *testing.T) {
	got := configVars(t, "cfg.ts", `function f() {
  const a = process.env.DATABASE_URL;
  const b = process.env["REDIS_ADDR"];
  const c = process.env.PORT || "8080";  // still extracted here; PORT is noise at resolve time
  const d = someObj.env.SPOOF;            // not process.env → skipped
}`)
	for _, want := range []string{"DATABASE_URL", "REDIS_ADDR", "PORT"} {
		if !got[want] {
			t.Errorf("recall: missing TS env var %q (got %v)", want, keys(got))
		}
	}
	if got["SPOOF"] {
		t.Error("precision: someObj.env.SPOOF must not be read as process.env")
	}
	if got["env"] {
		t.Error("precision: the process.env member node itself must not be read as a var")
	}
}
