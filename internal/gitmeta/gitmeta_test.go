package gitmeta

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run executes a git command in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// Keep the repo hermetic — don't let the developer's global git config
	// (signing, hooks, template dirs) leak into the temp repo.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestProvenance builds a temp git repo with an origin remote and one commit,
// then asserts Provenance carries the origin URL and a non-empty ref.
func TestProvenance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init")
	run(t, dir, "remote", "add", "origin", "https://github.com/x/svc-a.git")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-m", "init")

	origin, ref := Provenance(dir)
	if origin != "https://github.com/x/svc-a.git" {
		t.Errorf("origin = %q, want the configured remote URL", origin)
	}
	// The default branch name varies by git version (main/master), so assert the
	// ref resolved to *something* real and isn't the detached-HEAD sentinel.
	if ref == "" || ref == "HEAD" {
		t.Errorf("ref = %q, want a branch name or short SHA", ref)
	}
}

// TestProvenanceNonRepo proves the graceful-degrade contract: a directory that is
// not a git repo yields empty origin and ref (the caller then omits the world).
func TestProvenanceNonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin, ref := Provenance(t.TempDir())
	if origin != "" || ref != "" {
		t.Errorf("non-repo dir: got origin=%q ref=%q, want both empty", origin, ref)
	}
}
