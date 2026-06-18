package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// preCommitScriptPath resolves the absolute path to the real scripts/pre-commit
// relative to this test file, so it works regardless of the test's cwd or which
// worktree it runs in.
func preCommitScriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(PreCommitScript(repoRoot))
	if err != nil {
		t.Fatalf("abs(%q): %v", PreCommitScript(repoRoot), err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("pre-commit script not found at %s: %v", abs, err)
	}
	return abs
}

// TestPreCommitScrubsGitDir is the QUM-836 regression: git exports GIT_DIR (and
// friends) into the pre-commit hook env, and `make validate` → `go test` spawns
// nested git that inherits those vars and resolves to the OUTER repo instead of
// its temp repos. scripts/pre-commit must unset the repo-scoping GIT_* vars
// before invoking `make validate`, so the validation subprocess sees a clean env.
//
// This drives behavior, not script text: it stubs `make validate` with a
// Makefile that echoes the inherited GIT_DIR and asserts it is empty.
func TestPreCommitScrubsGitDir(t *testing.T) {
	preCommit := preCommitScriptPath(t)

	// A temp repo on a feature branch so the guard (which runs before the unset
	// and intentionally uses the committing worktree's git context) allows the
	// run regardless of identity.
	repo := initRepoOnBranch(t, "feature-branch", true)

	// Stub `make validate` to report, for each repo-scoping var, whether it is
	// PRESENT in the subprocess environment. We assert absence rather than an
	// empty value so an `export GIT_DIR=""` (still present, empty) does NOT pass:
	// `env | grep '^GIT_DIR='` matches an empty-string export but not an absent
	// var, which only a real `unset` produces.
	// Cover every repo-scoping var the hook unsets, so dropping any from the
	// unset line is caught.
	scrubbed := []string{
		"GIT_DIR", "GIT_INDEX_FILE", "GIT_WORK_TREE",
		"GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX",
	}
	makefile := "validate:\n\t@for v in " + strings.Join(scrubbed, " ") + "; do \\\n" +
		"\t\tif env | grep -q \"^$$v=\"; then echo \"LEAKED:$$v\"; else echo \"OK:$$v\"; fi; \\\n" +
		"\tdone\n"
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	// Run the real pre-commit hook with leaked GIT_* vars exported, exactly as git
	// does when it invokes the hook. Inject every scrubbed var so the test fails
	// if the hook forgets any of them.
	leaked := filepath.Join(repo, ".git")
	cmd := exec.Command(preCommit)
	cmd.Dir = repo
	cmd.Env = baseEnv(
		"SPRAWL_AGENT_IDENTITY=engineer",
		"GIT_DIR="+leaked,
		"GIT_INDEX_FILE="+filepath.Join(leaked, "index"),
		"GIT_WORK_TREE="+repo,
		"GIT_OBJECT_DIRECTORY="+filepath.Join(leaked, "objects"),
		"GIT_COMMON_DIR="+leaked,
		"GIT_NAMESPACE=leaked-ns",
		"GIT_PREFIX=leaked/",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pre-commit failed: %s: %v", out, err)
	}

	if strings.Contains(string(out), "LEAKED:") {
		t.Errorf("expected pre-commit to scrub repo-scoping GIT_* vars before `make validate`; a var leaked into its environment:\n%s", out)
	}
	for _, v := range scrubbed {
		if !strings.Contains(string(out), "OK:"+v) {
			t.Errorf("expected %s absent from `make validate` environment (OK:%s); got output:\n%s", v, v, out)
		}
	}
}
