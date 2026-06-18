package githooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// guardScriptPath resolves the absolute path to the real scripts/guard-main-commit
// relative to this test file, so it works regardless of the test's cwd or which
// worktree it runs in.
func guardScriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	abs, err := filepath.Abs(GuardMainCommitScript(repoRoot))
	if err != nil {
		t.Fatalf("abs(%q): %v", GuardMainCommitScript(repoRoot), err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("guard script not found at %s: %v", abs, err)
	}
	return abs
}

// baseEnv returns the process environment with any inherited SPRAWL_AGENT_IDENTITY
// stripped (so the agent running `make test` can't pollute the cases) plus a
// hermetic git author/committer identity. Extra entries are appended last and win.
func baseEnv(extra ...string) []string {
	// Repo-scoping GIT_* vars leaked from the caller (e.g. git exports GIT_DIR
	// into the pre-commit hook that runs `make test`) would point the nested git
	// in these tests at the outer repo instead of the temp repo (QUM-836). Strip
	// them so the suite is hermetic; callers can still inject specific values via
	// `extra`, which is appended last and wins.
	stripPrefixes := []string{
		"SPRAWL_AGENT_IDENTITY=",
		"GIT_DIR=", "GIT_INDEX_FILE=", "GIT_WORK_TREE=",
		"GIT_OBJECT_DIRECTORY=", "GIT_COMMON_DIR=", "GIT_NAMESPACE=", "GIT_PREFIX=",
	}
	var env []string
	for _, kv := range os.Environ() {
		skip := false
		for _, p := range stripPrefixes {
			if strings.HasPrefix(kv, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	return append(env, extra...)
}

func gitRun(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// initRepoOnBranch creates a temp git repo whose checked-out branch is `branch`.
// If withCommit is true an initial empty commit is created (so HEAD exists);
// otherwise the repo has no commits yet (exercises the no-HEAD edge case).
func initRepoOnBranch(t *testing.T, branch string, withCommit bool) string {
	t.Helper()
	dir := t.TempDir()
	env := baseEnv()
	if out, err := gitRun(t, dir, env, "init"); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	// Point HEAD at the desired branch even before any commit exists.
	if out, err := gitRun(t, dir, env, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		t.Fatalf("git symbolic-ref: %s: %v", out, err)
	}
	if withCommit {
		if out, err := gitRun(t, dir, env, "commit", "--allow-empty", "-m", "initial"); err != nil {
			t.Fatalf("git commit initial: %s: %v", out, err)
		}
	}
	return dir
}

// TestGuardScript_Unit execs the real guard script directly against temp repos
// and asserts the exit code per the QUM-808 acceptance criteria.
func TestGuardScript_Unit(t *testing.T) {
	guard := guardScriptPath(t)

	cases := []struct {
		name       string
		branch     string
		identity   string // "" means unset
		setID      bool
		wantBlock  bool
		wantPhrase string // substring expected on stderr when blocked
	}{
		{name: "main + non-root agent => blocked", branch: "main", identity: "engineer", setID: true, wantBlock: true, wantPhrase: "refusing commit"},
		{name: "main + weave => allowed", branch: "main", identity: "weave", setID: true, wantBlock: false},
		{name: "main + unset identity => allowed", branch: "main", setID: false, wantBlock: false},
		{name: "feature branch + non-root agent => allowed", branch: "feature-branch", identity: "engineer", setID: true, wantBlock: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoOnBranch(t, tc.branch, true)
			env := baseEnv()
			if tc.setID {
				env = append(env, "SPRAWL_AGENT_IDENTITY="+tc.identity)
			}
			cmd := exec.Command(guard)
			cmd.Dir = repo
			cmd.Env = env
			out, err := cmd.CombinedOutput()

			if tc.wantBlock {
				if err == nil {
					t.Fatalf("expected guard to block (non-zero exit), got success; output: %s", out)
				}
				if tc.wantPhrase != "" && !strings.Contains(string(out), tc.wantPhrase) {
					t.Errorf("expected guard message to mention %q; got: %s", tc.wantPhrase, out)
				}
			} else if err != nil {
				t.Fatalf("expected guard to allow (zero exit), got error %v; output: %s", err, out)
			}
		})
	}
}

// TestGuardScript_NoCommitsYet ensures the guard blocks a non-root agent on main
// even on a fresh repo with no HEAD commit (symbolic-ref path, not rev-parse).
func TestGuardScript_NoCommitsYet(t *testing.T) {
	guard := guardScriptPath(t)
	repo := initRepoOnBranch(t, "main", false)

	cmd := exec.Command(guard)
	cmd.Dir = repo
	cmd.Env = baseEnv("SPRAWL_AGENT_IDENTITY=engineer")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected guard to block on fresh main repo, got success; output: %s", out)
	}
}

// TestGuardHook_RealCommitRefused reproduces the QUM-808 incident: a non-root
// agent with cwd at the repo root (absolute temp path) running `git commit`.
// The guard is installed as the real .git/hooks/pre-commit and a real commit is
// attempted. It must be refused and must not land on main; weave must succeed.
func TestGuardHook_RealCommitRefused(t *testing.T) {
	guard := guardScriptPath(t)
	repo := initRepoOnBranch(t, "main", true)

	// Install the real guard as the pre-commit hook (copy + chmod, hermetic).
	guardBytes, err := os.ReadFile(guard)
	if err != nil {
		t.Fatalf("read guard: %v", err)
	}
	hookPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, guardBytes, 0o755); err != nil {
		t.Fatalf("install hook: %v", err)
	}

	commitCount := func(env []string) string {
		out, _ := gitRun(t, repo, env, "rev-list", "--count", "HEAD")
		return strings.TrimSpace(out)
	}

	before := commitCount(baseEnv())

	// Stage a change, then attempt the blocked commit as a non-root agent.
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=engineer"), "add", "file.txt"); err != nil {
		t.Fatalf("git add: %s: %v", out, err)
	}

	out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=engineer"), "commit", "-m", "should be refused")
	if err == nil {
		t.Fatalf("expected commit to be refused by hook, but it succeeded; output: %s", out)
	}
	if !strings.Contains(out, "refusing commit") {
		t.Errorf("expected guard message %q in hook output; got: %s", "refusing commit", out)
	}
	if got := commitCount(baseEnv()); got != before {
		t.Fatalf("commit landed on main despite guard: count %s -> %s", before, got)
	}

	// weave (root) must be allowed to commit to main.
	if out, err := gitRun(t, repo, baseEnv("SPRAWL_AGENT_IDENTITY=weave"), "commit", "-m", "weave allowed"); err != nil {
		t.Fatalf("expected weave commit to succeed; output: %s: %v", out, err)
	}
	if got := commitCount(baseEnv()); got == before {
		t.Fatalf("weave commit did not land: count stayed %s", before)
	}
}
