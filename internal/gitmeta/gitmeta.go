// Package gitmeta captures per-world git provenance — the repo's origin remote
// URL and the checked-out ref — so lodestar can emit where each world came from
// and what was extracted (branch-aware world-graph, #298). Everything here is
// best-effort: a non-git directory, or a repo without an "origin" remote, yields
// empty strings and the caller simply omits that world from world_meta.
package gitmeta

import (
	"os/exec"
	"strings"
)

// Provenance reads the git origin remote URL and current ref for dir. Both are
// best-effort and independent: either may be empty (dir is not a git repo, has no
// origin remote, or git is unavailable). The ref is the branch name, falling back
// to a short commit SHA when HEAD is detached (rev-parse --abbrev-ref returns the
// literal "HEAD" in that case).
func Provenance(dir string) (origin, ref string) {
	origin = gitOut(dir, "remote", "get-url", "origin")
	ref = gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if ref == "" || ref == "HEAD" { // detached HEAD (or failure) → short SHA
		ref = gitOut(dir, "rev-parse", "--short", "HEAD")
	}
	return origin, ref
}

// gitOut runs `git -C dir <args...>` and returns trimmed stdout, or "" on any
// failure (git missing, not a repo, no such remote). Degrading to "" is the
// contract — provenance is a bonus, never a hard dependency of extraction.
func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
