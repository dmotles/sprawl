package merge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepoForRealDeps builds a throwaway repo with two commits and returns its
// path plus both SHAs (base, tip). `side` is a branch left at base.
func gitRepoForRealDeps(t *testing.T) (dir, base, tip string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@x",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@x",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "--", "a.txt")
	run("commit", "-q", "-m", "base")
	base = run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "--", "b.txt")
	run("commit", "-q", "-m", "tip")
	tip = run("rev-parse", "HEAD")
	run("branch", "side", base)
	return dir, base, tip
}

// TestRealGitIsAncestor_DistinguishesFalseFromBroken is the reason this seam is
// not a one-liner.
//
// `git merge-base --is-ancestor` answers FALSE by exiting 1, which is
// indistinguishable from a failure to any wrapper that treats non-zero as an
// error. The caller's two responses are opposite — a false answer is a rebase to
// diagnose, an error is a broken repository to report — so collapsing them makes
// the engine report one as the other. Both directions plus the error case are
// asserted here, against real git.
func TestRealGitIsAncestor_DistinguishesFalseFromBroken(t *testing.T) {
	dir, base, tip := gitRepoForRealDeps(t)

	t.Run("true when the ancestor really is one", func(t *testing.T) {
		ok, err := RealGitIsAncestor(dir, base, tip)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Errorf("base %s should be an ancestor of tip %s", base[:8], tip[:8])
		}
	})
	t.Run("FALSE, not an error, in the other direction", func(t *testing.T) {
		// The asymmetry that matters. This is the argument order the engine
		// must NOT use, and it has to come back as a clean false rather than as
		// an error that would be surfaced as a broken repository.
		ok, err := RealGitIsAncestor(dir, tip, base)
		if err != nil {
			t.Fatalf("exit 1 is git's FALSE answer and must not surface as an error: %v", err)
		}
		if ok {
			t.Error("tip is not an ancestor of base; got true")
		}
	})
	t.Run("true for a commit and itself", func(t *testing.T) {
		// Load-bearing for the ff predicate: this IS the already-up-to-date
		// case, and it is why --is-ancestor alone cannot prove the parent
		// moved. The engine pairs it with SHA equality for exactly this reason.
		ok, err := RealGitIsAncestor(dir, tip, tip)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("a commit must be its own ancestor")
		}
	})
	t.Run("a real failure IS an error", func(t *testing.T) {
		ok, err := RealGitIsAncestor(dir, "0000000000000000000000000000000000000000", tip)
		if err == nil {
			t.Error("an unresolvable revision must be an error, not a false answer — otherwise a typo reads as a legitimate 'not an ancestor'")
		}
		if ok {
			t.Error("must not report true on failure")
		}
	})
}

// TestRealGitRevParseRef_RefusesAnUnresolvableRev pins the --verify guard.
//
// A bare `git rev-parse <unknown>` ECHOES ITS ARGUMENT BACK and exits 0, so
// without --verify this seam would return the input string as if it were a SHA.
// The ff-merge predicate would then compare a branch name against a real SHA
// and refuse a perfectly good merge with an incomprehensible message.
func TestRealGitRevParseRef_RefusesAnUnresolvableRev(t *testing.T) {
	dir, base, tip := gitRepoForRealDeps(t)

	t.Run("resolves a branch name to a full SHA", func(t *testing.T) {
		got, err := RealGitRevParseRef(dir, "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != tip {
			t.Errorf("main = %q, want %q", got, tip)
		}
		if len(got) != 40 {
			t.Errorf("want a full 40-char SHA, got %d chars (%q): an abbreviated SHA's length depends on repository size, so one that is unambiguous in a fixture can be ambiguous in a real repo", len(got), got)
		}
	})
	t.Run("resolves a different branch to a different SHA", func(t *testing.T) {
		// Distinctness, so a "returns the same thing regardless" bug cannot
		// pass the subtest above.
		got, err := RealGitRevParseRef(dir, "side")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != base {
			t.Errorf("side = %q, want %q", got, base)
		}
	})
	t.Run("an unknown ref is an error, not an echo", func(t *testing.T) {
		got, err := RealGitRevParseRef(dir, "no-such-branch")
		if err == nil {
			t.Errorf("want an error for an unresolvable ref, got %q — a bare rev-parse echoes its argument and exits 0", got)
		}
		if strings.Contains(got, "no-such-branch") {
			t.Errorf("returned the input string as though it were a SHA: %q", got)
		}
	})
}
